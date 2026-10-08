// Package skills manages Core resources. Local installation belongs to the
// external skills installer; this package never executes a provider or worker.
package skills

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/credentials"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/shared"
	"github.com/spf13/cobra"
)

var idPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

const maxResponse = 8 * 1024 * 1024 // owned list: up to 200 packages with version metadata
const maxBundle = 64 * 1024

func validID(id string) bool {
	return idPattern.MatchString(id) && id != "00000000-0000-0000-0000-000000000000"
}
func requireID(id, flag string) error {
	if !validID(id) {
		return fmt.Errorf("--%s requires a UUID", flag)
	}
	return nil
}

type client struct {
	base, token string
	http        *http.Client
}

func newClient(opts shared.GlobalOptions, owned bool) (*client, error) {
	base, err := credentials.NormalizeAPI(opts.APIBase)
	if err != nil {
		return nil, err
	}
	token := ""
	if owned {
		token = strings.TrimSpace(opts.UserToken)
		if !strings.HasPrefix(token, "ol_user_") {
			return nil, errors.New("a Core User Token is required; use auth login with the required skill scopes")
		}
	}
	return &client{base: base, token: token, http: &http.Client{Timeout: opts.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *client) request(ctx context.Context, method, path string, payload any, limit int) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, errors.New("invalid skill request")
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/api/v1"+path, body)
	if err != nil {
		return nil, errors.New("invalid Core URL")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", shared.SDKAgent)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, errors.New("cannot reach Core skill API")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, int64(limit)+1))
	if err != nil || len(raw) > limit {
		return nil, errors.New("invalid or oversized Core skill response")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		switch res.StatusCode {
		case 401:
			return nil, errors.New("Core skill API rejected the credential; sign in again")
		case 403:
			return nil, errors.New("Core skill permission denied; check skill scopes and Agent resource range")
		case 404:
			return nil, errors.New("skill resource unavailable; check ownership, visibility and publication")
		case 409:
			return nil, errors.New("skill conflict; check the expected digest and version")
		default:
			return nil, fmt.Errorf("Core skill API failed (HTTP %d)", res.StatusCode)
		}
	}
	if res.StatusCode == http.StatusNoContent {
		return []byte(`{"status":"unbound"}`), nil
	}
	if !json.Valid(raw) {
		return nil, errors.New("invalid Core skill JSON response")
	}
	return raw, nil
}
func writeResponse(streams shared.IO, raw []byte) error {
	return shared.WriteJSON(streams.Stdout, json.RawMessage(raw))
}
func contextFor(cmd *cobra.Command, opts *shared.GlobalOptions) (context.Context, context.CancelFunc) {
	return context.WithTimeout(cmd.Context(), opts.Timeout)
}

func New(streams shared.IO, opts *shared.GlobalOptions) *cobra.Command {
	root := &cobra.Command{Use: "skills", Short: "Query, download, import and bind platform Skill packages (local install: npx skills)", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		return errors.New("skills requires list, get, download, import, bindings, bind or unbind")
	}}
	var owned bool
	var query string
	var page, size int
	list := &cobra.Command{Use: "list", Short: "List public packages, or your packages with --owned", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if owned && (cmd.Flags().Changed("query") || cmd.Flags().Changed("page") || cmd.Flags().Changed("limit")) {
			return errors.New("--owned returns the Core owned list; query/page/limit apply only to public discovery")
		}
		if page < 1 || page > 10000 || size < 1 || size > 50 || len([]rune(query)) > 200 {
			return errors.New("page must be 1..10000, limit 1..50, query at most 200 characters")
		}
		c, err := newClient(*opts, owned)
		if err != nil {
			return err
		}
		ctx, stop := contextFor(cmd, opts)
		defer stop()
		path := "/creator/skill-packages"
		if !owned {
			q := url.Values{"q": {query}, "page": {fmt.Sprint(page)}, "size": {fmt.Sprint(size)}}
			path = "/skill-packages?" + q.Encode()
		}
		raw, err := c.request(ctx, "GET", path, nil, maxResponse)
		if err != nil {
			return err
		}
		return writeResponse(streams, raw)
	}}
	list.Flags().BoolVar(&owned, "owned", false, "Read your packages (skill-packages:read)")
	list.Flags().StringVar(&query, "query", "", "Public name/description search")
	list.Flags().IntVar(&page, "page", 1, "Public page")
	list.Flags().IntVar(&size, "limit", 12, "Public page size, 1..50")
	root.AddCommand(list)
	var getID, getVersion string
	var getOwned bool
	get := &cobra.Command{Use: "get", Short: "Read package or fixed-version metadata", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireID(getID, "id"); err != nil {
			return err
		}
		if getVersion != "" {
			if err := requireID(getVersion, "version"); err != nil {
				return err
			}
		}
		c, err := newClient(*opts, getOwned)
		if err != nil {
			return err
		}
		ctx, stop := contextFor(cmd, opts)
		defer stop()
		prefix := "/skill-packages/"
		if getOwned {
			prefix = "/creator/skill-packages/"
		}
		raw, err := c.request(ctx, "GET", prefix+getID, nil, maxResponse)
		if err != nil {
			return err
		}
		if getVersion != "" {
			if getOwned {
				raw, err = selectOwnedVersion(raw, getVersion)
			} else {
				raw, err = c.request(ctx, "GET", prefix+getID+"/versions/"+getVersion+"/metadata", nil, maxResponse)
			}
			if err != nil {
				return err
			}
		}
		return writeResponse(streams, raw)
	}}
	get.Flags().StringVar(&getID, "id", "", "Package UUID")
	get.Flags().StringVar(&getVersion, "version", "", "Fixed version UUID")
	get.Flags().BoolVar(&getOwned, "owned", false, "Read your package metadata (skill-packages:read)")
	root.AddCommand(get)
	var downID, downVersion, output, expected string
	var downOwned bool
	download := &cobra.Command{Use: "download", Short: "Verify and save canonical bundle JSON to a new file; does not install", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireID(downID, "id"); err != nil {
			return err
		}
		if err := requireID(downVersion, "version"); err != nil {
			return err
		}
		if output == "" || output == "-" {
			return errors.New("--output requires a new file path")
		}
		if expected != "" && !digestPattern.MatchString(expected) {
			return errors.New("--digest requires lowercase SHA-256")
		}
		c, err := newClient(*opts, downOwned)
		if err != nil {
			return err
		}
		ctx, stop := contextFor(cmd, opts)
		defer stop()
		prefix := "/skill-packages/"
		if downOwned {
			prefix = "/creator/skill-packages/"
		}
		var metadata []byte
		if downOwned {
			var detail []byte
			detail, err = c.request(ctx, "GET", prefix+downID, nil, maxResponse)
			if err == nil {
				metadata, err = selectOwnedVersion(detail, downVersion)
			}
		} else {
			metadata, err = c.request(ctx, "GET", prefix+downID+"/versions/"+downVersion+"/metadata", nil, maxResponse)
		}
		if err != nil {
			return err
		}
		var v struct{ ID, Digest string }
		if json.Unmarshal(metadata, &v) != nil || !strings.EqualFold(v.ID, downVersion) || !digestPattern.MatchString(v.Digest) {
			return errors.New("invalid version digest metadata")
		}
		if expected != "" && expected != v.Digest {
			return errors.New("version does not match --digest")
		}
		path := prefix + downID + "/versions/" + downVersion
		if !downOwned {
			path += "/bundle.json"
		}
		raw, err := c.request(ctx, "GET", path, nil, maxBundle)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(raw)
		if hex.EncodeToString(hash[:]) != v.Digest {
			return errors.New("download digest mismatch; no file written")
		}
		absolute, err := saveNewBundle(output, raw)
		if err != nil {
			return err
		}
		return shared.WriteJSON(streams.Stdout, map[string]any{"path": absolute, "package_id": downID, "version_id": downVersion, "digest": v.Digest, "format": "openlinker.skill-bundle.v1"})
	}}
	download.Flags().StringVar(&downID, "id", "", "Package UUID")
	download.Flags().StringVar(&downVersion, "version", "", "Fixed version UUID")
	download.Flags().StringVar(&output, "output", "", "New output file (never overwritten)")
	download.Flags().StringVar(&expected, "digest", "", "Expected lowercase SHA-256 from a trusted fixed reference")
	download.Flags().BoolVar(&downOwned, "owned", false, "Download your private files (skill-packages:read)")
	root.AddCommand(download)
	var sourceID, sourceVersion, sourceDigest string
	imp := &cobra.Command{Use: "import", Short: "Copy a published fixed version into your private packages", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireID(sourceID, "id"); err != nil {
			return err
		}
		if err := requireID(sourceVersion, "version"); err != nil {
			return err
		}
		if !digestPattern.MatchString(sourceDigest) {
			return errors.New("--digest requires the expected lowercase SHA-256")
		}
		c, err := newClient(*opts, true)
		if err != nil {
			return err
		}
		ctx, stop := contextFor(cmd, opts)
		defer stop()
		raw, err := c.request(ctx, "POST", "/creator/skill-packages/imports", map[string]string{"source_package_id": sourceID, "source_version_id": sourceVersion, "expected_digest": sourceDigest}, maxResponse)
		if err != nil {
			return err
		}
		return writeResponse(streams, raw)
	}}
	imp.Flags().StringVar(&sourceID, "id", "", "Published package UUID")
	imp.Flags().StringVar(&sourceVersion, "version", "", "Published version UUID")
	imp.Flags().StringVar(&sourceDigest, "digest", "", "Expected SHA-256 (required; skill-packages:import)")
	root.AddCommand(imp)
	for _, op := range []string{"bindings", "bind", "unbind"} {
		var agentID, packageID, versionID string
		command := &cobra.Command{Use: op, Short: map[string]string{"bindings": "Read an owned Agent's bindings (skill-bindings:read)", "bind": "Bind an owned private fixed version (skill-bindings:manage)", "unbind": "Remove an owned Agent's package binding (skill-bindings:manage)"}[op], Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireID(agentID, "agent"); err != nil {
				return err
			}
			if op != "bindings" {
				if err := requireID(packageID, "id"); err != nil {
					return err
				}
			}
			if op == "bind" {
				if err := requireID(versionID, "version"); err != nil {
					return err
				}
			}
			c, err := newClient(*opts, true)
			if err != nil {
				return err
			}
			ctx, stop := contextFor(cmd, opts)
			defer stop()
			path := "/creator/agents/" + agentID + "/skill-packages"
			method := "GET"
			var payload any
			if op != "bindings" {
				path += "/" + packageID
				method = "DELETE"
			}
			if op == "bind" {
				method = "PUT"
				payload = map[string]string{"version_id": versionID}
			}
			raw, err := c.request(ctx, method, path, payload, maxResponse)
			if err != nil {
				return err
			}
			return writeResponse(streams, raw)
		}}
		command.Flags().StringVar(&agentID, "agent", "", "Owned Agent UUID")
		if op != "bindings" {
			command.Flags().StringVar(&packageID, "id", "", "Owned private package UUID")
		}
		if op == "bind" {
			command.Flags().StringVar(&versionID, "version", "", "Explicit fixed version UUID")
		}
		root.AddCommand(command)
	}
	return root
}
func selectOwnedVersion(detail []byte, id string) ([]byte, error) {
	var p struct {
		Versions []json.RawMessage `json:"versions"`
	}
	if json.Unmarshal(detail, &p) != nil {
		return nil, errors.New("invalid package metadata")
	}
	for _, raw := range p.Versions {
		var v struct{ ID string }
		if json.Unmarshal(raw, &v) == nil && strings.EqualFold(v.ID, id) {
			return raw, nil
		}
	}
	return nil, errors.New("version does not belong to your package")
}

// saveNewBundle publishes fully written bytes without ever replacing a file.
// Reject symlink ancestors, pin the opened parent's identity, then use an
// exclusive private temporary file and a no-overwrite hard link through its FD.
func saveNewBundle(output string, raw []byte) (string, error) {
	absolute, err := filepath.Abs(output)
	if err != nil {
		return "", errors.New("invalid output path")
	}
	parent := filepath.Dir(absolute)
	var info os.FileInfo
	for current := parent; ; current = filepath.Dir(current) {
		info, err = os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("output parent must exist and have no symlink ancestors")
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	expected, err := os.Lstat(parent)
	if err != nil {
		return "", errors.New("cannot inspect output directory")
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return "", errors.New("cannot open output directory")
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(expected, opened) {
		return "", errors.New("output directory changed")
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return "", errors.New("cannot create download file")
	}
	temporary := ".openlinker-skill-" + hex.EncodeToString(nonce)
	f, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", errors.New("cannot create download file")
	}
	defer root.Remove(temporary)
	_, writeErr := f.Write(raw)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return "", errors.New("cannot write download file")
	}
	if err = root.Link(temporary, filepath.Base(absolute)); err != nil {
		return "", errors.New("cannot publish download: output exists or filesystem does not support hard links")
	}
	return absolute, nil
}

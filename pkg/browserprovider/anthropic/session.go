package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
)

const sessionContractID = "openlinker.browser.provider.anthropic.session.v1"

type sessionEnvelope struct {
	ContractID string    `json:"contract_id"`
	Messages   []message `json:"messages"`
}

// MarshalSession returns opaque Provider state for the encrypted local
// Provider-session store. The returned bytes must never be written to Core
// events, Run output, or ordinary logs.
func (adapter *Adapter) MarshalSession(
	session *Session,
) ([]byte, *browserprotocol.Failure) {
	if adapter == nil || session == nil {
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorConversationRecovery,
			"Anthropic browser session is not configured",
			false,
		)
	}
	if failure := adapter.validateHistory(session.messages); failure != nil {
		return nil, failure
	}
	raw, err := json.Marshal(sessionEnvelope{
		ContractID: sessionContractID,
		Messages:   session.messages,
	})
	if err != nil || len(raw) > adapter.config.MaxHistoryBytes {
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorConversationRecovery,
			"Anthropic browser session could not be encoded safely",
			false,
		)
	}
	return raw, nil
}

func (adapter *Adapter) RestoreSession(
	raw []byte,
) (*Session, *browserprotocol.Failure) {
	if adapter == nil || len(raw) == 0 || len(raw) > adapter.config.MaxHistoryBytes {
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorConversationRecovery,
			"Anthropic browser session cannot be recovered safely",
			false,
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope sessionEnvelope
	if err := decoder.Decode(&envelope); err != nil ||
		envelope.ContractID != sessionContractID {
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorConversationRecovery,
			"Anthropic browser session cannot be recovered safely",
			false,
		)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorConversationRecovery,
			"Anthropic browser session contains trailing data",
			false,
		)
	}
	if failure := adapter.validateHistory(envelope.Messages); failure != nil {
		return nil, failure
	}
	return &Session{messages: envelope.Messages}, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return errors.New("trailing JSON value")
	}
	return err
}

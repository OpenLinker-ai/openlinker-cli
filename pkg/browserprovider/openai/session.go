package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprotocol"
	"github.com/OpenLinker-ai/openlinker-cli/pkg/browserprovider"
)

const sessionContractID = "openlinker.browser.provider.openai.session.v1"

type sessionEnvelope struct {
	ContractID         string `json:"contract_id"`
	PreviousResponseID string `json:"previous_response_id,omitempty"`
}

// MarshalSession returns opaque Provider state for the encrypted local
// Provider-session store. The returned bytes must never be written to Core
// events, Run output, or ordinary logs.
func (adapter *Adapter) MarshalSession(
	session *Session,
) ([]byte, *browserprotocol.Failure) {
	if adapter == nil || session == nil {
		return nil, browserprovider.ProviderOutputFailure(
			"OpenAI browser session is not configured",
		)
	}
	if len(session.previousResponseID) > 512 {
		return nil, browserprovider.ProviderOutputFailure(
			"OpenAI browser session identifier is invalid",
		)
	}
	raw, err := json.Marshal(sessionEnvelope{
		ContractID:         sessionContractID,
		PreviousResponseID: session.previousResponseID,
	})
	if err != nil {
		return nil, browserprovider.ProviderOutputFailure(
			"OpenAI browser session could not be encoded",
		)
	}
	return raw, nil
}

func (adapter *Adapter) RestoreSession(
	raw []byte,
) (*Session, *browserprotocol.Failure) {
	if adapter == nil || len(raw) == 0 || len(raw) > 4096 {
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorConversationRecovery,
			"OpenAI browser session cannot be recovered safely",
			false,
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope sessionEnvelope
	if err := decoder.Decode(&envelope); err != nil ||
		envelope.ContractID != sessionContractID ||
		len(envelope.PreviousResponseID) > 512 {
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorConversationRecovery,
			"OpenAI browser session cannot be recovered safely",
			false,
		)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorConversationRecovery,
			"OpenAI browser session contains trailing data",
			false,
		)
	}
	return &Session{previousResponseID: envelope.PreviousResponseID}, nil
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

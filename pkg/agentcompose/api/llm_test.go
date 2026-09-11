package api

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

type llmGeneratorStub struct {
	err error
}

func (s llmGeneratorStub) Generate(context.Context, string, string, string) (llms.GenerateResult, error) {
	return llms.GenerateResult{}, s.err
}

func TestLLMHandlerMapsClassifiedErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code connect.Code
	}{
		{
			name: "configuration failure",
			err:  domain.ClassifyError(domain.ErrFailedPrecondition, "invalid LLM configuration", nil),
			code: connect.CodeFailedPrecondition,
		},
		{name: "unexpected failure", err: errors.New("boom"), code: connect.CodeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewLLMHandler(llmGeneratorStub{err: tt.err}, nil)
			_, err := handler.Generate(context.Background(), connect.NewRequest(&agentcomposev2.GenerateLLMRequest{}))
			if got := connect.CodeOf(err); got != tt.code {
				t.Fatalf("Generate() code = %s, want %s: %v", got, tt.code, err)
			}
		})
	}
}

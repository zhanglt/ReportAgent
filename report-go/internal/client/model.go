package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type Model struct {
	text       *einoopenai.ChatModel
	structured *einoopenai.ChatModel
	stream     bool
}

func NewModel(ctx context.Context, httpClient *http.Client, baseURL, apiKey, name string, stream bool) (*Model, error) {
	config := einoopenai.ChatModelConfig{
		APIKey: apiKey, BaseURL: strings.TrimRight(baseURL, "/"), Model: name, HTTPClient: httpClient,
	}
	text, err := einoopenai.NewChatModel(ctx, &config)
	if err != nil {
		return nil, err
	}
	config.ResponseFormat = &einoopenai.ChatCompletionResponseFormat{Type: einoopenai.ChatCompletionResponseFormatTypeJSONObject}
	structured, err := einoopenai.NewChatModel(ctx, &config)
	if err != nil {
		return nil, err
	}
	return &Model{text: text, structured: structured, stream: stream}, nil
}

func (m *Model) Complete(ctx context.Context, system string, input []byte, temperature float32, structured bool) (string, error) {
	chat := m.text
	if structured {
		chat = m.structured
	}
	messages := []*schema.Message{schema.SystemMessage(system), schema.UserMessage(string(input))}
	option := model.WithTemperature(temperature)
	if !m.stream {
		msg, err := chat.Generate(ctx, messages, option)
		if err != nil {
			return "", err
		}
		return msg.Content, nil
	}
	stream, err := chat.Stream(ctx, messages, option)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	var content strings.Builder
	for {
		part, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return content.String(), nil
		}
		if err != nil {
			return "", err
		}
		content.WriteString(part.Content)
	}
}

// ToolTurn runs one Eino tool-calling model turn, including streamed tool arguments.
func (m *Model) ToolTurn(ctx context.Context, messages []*schema.Message, tools []*schema.ToolInfo) (*schema.Message, error) {
	opts := []model.Option{model.WithTools(tools), model.WithTemperature(0)}
	if !m.stream {
		return m.text.Generate(ctx, messages, opts...)
	}
	parts, err := m.text.Stream(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	defer parts.Close()
	var messagesOut []*schema.Message
	for {
		msg, err := parts.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		messagesOut = append(messagesOut, msg)
	}
	if len(messagesOut) == 0 {
		return nil, errors.New("model returned no response")
	}
	return schema.ConcatMessages(messagesOut)
}

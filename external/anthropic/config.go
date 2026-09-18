package anthropic

import (
	"errors"
	"net/http"
)

// The hardcoded deployment. There is no environment lookup and no config file
// yet: [Defaults] is the one place these values live, and a later config layer
// fills the same [Config] from wherever it likes.
//
// The names in the comments are the environment variables Claude Code reads for
// the same settings, so the mapping is obvious when that layer arrives.
const (
	// ANTHROPIC_BASE_URL. Ollama serves the Anthropic Messages API under /v1.
	defaultBaseURL = "http://localhost:11434"

	// ANTHROPIC_AUTH_TOKEN. Ollama ignores the value but wants the header.
	defaultAuthToken = "ollama"

	// ANTHROPIC_API_KEY. Empty, because the auth token is what Ollama takes.
	defaultAPIKey = ""

	// ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU}_MODEL and
	// CLAUDE_CODE_SUBAGENT_MODEL. One model answers to every alias for now.
	defaultModel = "deepseek-v4-flash:cloud"

	// max_tokens is required by the wire protocol, so it needs a value even
	// though nothing has asked for one.
	defaultMaxTokens = 8192
)

// apiVersion is the anthropic-version header every request carries.
const apiVersion = "2023-06-01"

// Models maps the aliases a caller may put in Options.Model onto real model
// ids. Subagent is here because the host may hand it to a delegated session;
// this backend does not delegate on its own.
type Models struct {
	Opus     string
	Sonnet   string
	Haiku    string
	Subagent string
}

// Resolve turns an Options.Model value into a model id. Aliases map, an empty
// name means the sonnet slot, and anything else is already an id.
func (m Models) Resolve(name string) string {
	switch name {
	case "opus":
		return m.Opus
	case "sonnet", "":
		return m.Sonnet
	case "haiku":
		return m.Haiku
	case "subagent":
		return m.Subagent
	default:
		return name
	}
}

// Config is everything this backend needs to reach an Anthropic-shaped API.
// [Defaults] returns the hardcoded one; build your own to point somewhere else.
type Config struct {
	// BaseURL is the API root, without the /v1/messages path.
	BaseURL string

	// AuthToken is sent as "Authorization: Bearer". Empty leaves the header off.
	AuthToken string

	// APIKey is sent as "x-api-key". Empty leaves the header off.
	APIKey string

	// Models maps aliases onto model ids.
	Models Models

	// MaxTokens caps each response. The wire protocol requires it.
	MaxTokens int

	// HTTP is the client requests go out on. Nil uses http.DefaultClient.
	//
	// Leave its Timeout at zero: a turn is bounded by the context the caller
	// passes to Prompt, and a client timeout would cut long tool loops short.
	HTTP *http.Client
}

// Defaults is the hardcoded configuration: Ollama on this machine, speaking the
// Anthropic protocol, answering as DeepSeek flash.
func Defaults() Config {
	return Config{
		BaseURL:   defaultBaseURL,
		AuthToken: defaultAuthToken,
		APIKey:    defaultAPIKey,
		Models: Models{
			Opus:     defaultModel,
			Sonnet:   defaultModel,
			Haiku:    defaultModel,
			Subagent: defaultModel,
		},
		MaxTokens: defaultMaxTokens,
	}
}

// validate fails loudly on a config that would otherwise produce a confusing
// error at the first request.
func (c Config) validate() error {
	if c.BaseURL == "" {
		return errors.New("pi: anthropic: Config.BaseURL is empty")
	}
	if c.MaxTokens <= 0 {
		return errors.New("pi: anthropic: Config.MaxTokens must be positive")
	}
	return nil
}

func (c Config) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

package llm

import (
	"testing"

	"react-base-service/conf"
)

func setTestLLMConfig(t *testing.T, apiCfg conf.LLMApiConfig, models map[string]conf.ModelCatalog) {
	t.Helper()

	oldAPILLM := conf.API.LLM
	oldCustomLLM := conf.CustomConf.LLM

	conf.API.LLM = apiCfg
	conf.CustomConf.LLM = conf.LLMConfig{Models: models}

	t.Cleanup(func() {
		conf.API.LLM = oldAPILLM
		conf.CustomConf.LLM = oldCustomLLM
	})
}

func TestResolveApiKey(t *testing.T) {
	setTestLLMConfig(t, conf.LLMApiConfig{
		ApiKeys: map[string]string{
			"demo":   "minimax-demo-key",
			"legacy": "legacy-app-key",
		},
	}, nil)

	if got := ResolveApiKey("demo"); got != "minimax-demo-key" {
		t.Fatalf("ResolveApiKey(demo) = %q, want %q", got, "minimax-demo-key")
	}
	if got := ResolveApiKey("legacy"); got != "legacy-app-key" {
		t.Fatalf("ResolveApiKey(legacy) = %q, want %q", got, "legacy-app-key")
	}
}

func TestGetClient_MinimaxUsesPlatformAPIKey(t *testing.T) {
	setTestLLMConfig(t, conf.LLMApiConfig{
		ApiKeys: map[string]string{
			"demo":   "minimax-demo-key",
			"legacy": "legacy-app-key",
		},
		Endpoints: map[string]conf.EndpointConfig{
			"gpt": {
				ApiUrl:    "https://example.com/openai",
				MaxTokens: 4096,
			},
			"minimax": {
				ApiUrl:    "https://example.com/openai",
				MaxTokens: 4096,
			},
		},
	}, map[string]conf.ModelCatalog{
		"gpt": {
			DefaultVersion: "gpt-5.2",
		},
		"minimax": {
			DefaultVersion: "MiniMax-M2.5",
		},
	})

	client, err := GetClient("demo", "minimax")
	if err != nil {
		t.Fatalf("GetClient(demo, minimax) returned error: %v", err)
	}

	minimaxClient, ok := client.(*MiniMaxClient)
	if !ok {
		t.Fatalf("GetClient(demo, minimax) returned %T, want *MiniMaxClient", client)
	}
	if minimaxClient.apiKey != "minimax-demo-key" {
		t.Fatalf("apiKey = %q, want %q", minimaxClient.apiKey, "minimax-demo-key")
	}

	client, err = GetClient("demo", "gpt")
	if err != nil {
		t.Fatalf("GetClient(demo, gpt) returned error: %v", err)
	}

	gptClient, ok := client.(*GPTClient)
	if !ok {
		t.Fatalf("GetClient(demo, gpt) returned %T, want *GPTClient", client)
	}
	if gptClient.apiKey != "minimax-demo-key" {
		t.Fatalf("apiKey = %q, want %q", gptClient.apiKey, "minimax-demo-key")
	}
}

func TestGetClientWithKey_Minimax(t *testing.T) {
	setTestLLMConfig(t, conf.LLMApiConfig{
		Endpoints: map[string]conf.EndpointConfig{
			"minimax": {
				ApiUrl:    "https://example.com/openproxy-cn",
				MaxTokens: 4096,
			},
		},
	}, map[string]conf.ModelCatalog{
		"minimax": {
			DefaultVersion: "MiniMax-M2.5",
		},
	})

	client, err := GetClientWithKey("custom-minimax-key", "minimax")
	if err != nil {
		t.Fatalf("GetClientWithKey(custom-minimax-key, minimax) returned error: %v", err)
	}

	minimaxClient, ok := client.(*MiniMaxClient)
	if !ok {
		t.Fatalf("GetClientWithKey(custom-minimax-key, minimax) returned %T, want *MiniMaxClient", client)
	}
	if minimaxClient.apiKey != "custom-minimax-key" {
		t.Fatalf("apiKey = %q, want %q", minimaxClient.apiKey, "custom-minimax-key")
	}
}

func TestGetClient_DeepSeekUsesClaudeClient(t *testing.T) {
	setTestLLMConfig(t, conf.LLMApiConfig{
		ApiKeys: map[string]string{
			"demo": "deepseek-demo-key",
		},
		Endpoints: map[string]conf.EndpointConfig{
			"deepseek": {
				ApiUrl:    "https://api.deepseek.com/anthropic",
				MaxTokens: 32768,
			},
		},
	}, map[string]conf.ModelCatalog{
		"deepseek": {
			DefaultVersion: "deepseek-flash",
		},
	})

	client, err := GetClient("demo", "deepseek")
	if err != nil {
		t.Fatalf("GetClient(demo, deepseek) returned error: %v", err)
	}

	claudeClient, ok := client.(*ClaudeClient)
	if !ok {
		t.Fatalf("GetClient(demo, deepseek) returned %T, want *ClaudeClient", client)
	}
	if claudeClient.apiKey != "deepseek-demo-key" {
		t.Fatalf("apiKey = %q, want %q", claudeClient.apiKey, "deepseek-demo-key")
	}
	if claudeClient.config.ApiUrl != "https://api.deepseek.com/anthropic" {
		t.Fatalf("apiUrl = %q", claudeClient.config.ApiUrl)
	}
}

func TestGetClientWithUserModel_DeepSeekAnthropicEndpoint(t *testing.T) {
	setTestLLMConfig(t, conf.LLMApiConfig{
		Endpoints: map[string]conf.EndpointConfig{
			"claude": {
				ApiUrl:    "https://example.com/anthropic",
				MaxTokens: 4096,
			},
			"deepseek": {
				ApiUrl:    "https://api.deepseek.com/anthropic",
				MaxTokens: 32768,
			},
		},
	}, nil)

	client, err := GetClientWithUserModel("custom-deepseek-key", "DeepSeek Anthropic")
	if err != nil {
		t.Fatalf("GetClientWithUserModel(custom, DeepSeek Anthropic) returned error: %v", err)
	}

	claudeClient, ok := client.(*ClaudeClient)
	if !ok {
		t.Fatalf("GetClientWithUserModel(custom, DeepSeek Anthropic) returned %T, want *ClaudeClient", client)
	}
	if claudeClient.config.ApiUrl != "https://api.deepseek.com/anthropic" {
		t.Fatalf("apiUrl = %q", claudeClient.config.ApiUrl)
	}
}

func TestResolveModelVersion_MinimaxDefault(t *testing.T) {
	setTestLLMConfig(t, conf.LLMApiConfig{}, map[string]conf.ModelCatalog{
		"minimax": {
			DefaultVersion: "MiniMax-M2.5",
		},
	})

	if got := ResolveModelVersion("minimax", ""); got != "MiniMax-M2.5" {
		t.Fatalf("ResolveModelVersion(minimax, empty) = %q, want %q", got, "MiniMax-M2.5")
	}
	if got := ResolveModelVersion("minimax", "custom-version"); got != "custom-version" {
		t.Fatalf("ResolveModelVersion(minimax, custom-version) = %q, want %q", got, "custom-version")
	}
}

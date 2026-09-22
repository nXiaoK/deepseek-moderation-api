package audit

import (
	"context"
	"strings"
	"testing"
)

func TestModelNamesAllowGatewayAliases(t *testing.T) {
	for _, model := range []string{"~deepseek/deepseek-v4-flash-latest", "deepseek/deepseek-v4-flash-latest", "vendor/model:version_1.0", "deepseek-flash", "~" + strings.Repeat("a", 99)} {
		cfg := DefaultConfig()
		cfg.Model = model
		if err := cfg.Validate(); err != nil {
			t.Fatalf("model %q rejected: %v", model, err)
		}
	}
	for _, model := range []string{"", "~" + strings.Repeat("a", 100), " model", "model name", "model\n", "model\t", "model\x00", `\~deepseek/model`, `"model"`} {
		cfg := DefaultConfig()
		cfg.Model = model
		if err := cfg.Validate(); err == nil {
			t.Fatalf("invalid model %q accepted", model)
		}
	}
}

func TestChannelPersistsGatewayModelAlias(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	id, err := store.SaveCredential(ctx, "admin", "", "gateway", "test-api-key", true)
	if err != nil {
		t.Fatal(err)
	}
	const model = "~deepseek/deepseek-v4-flash-latest"
	channel := createTestChannel(t, store, id, "gateway", model)
	channels, err := store.Channels(ctx)
	if err != nil || len(channels) != 1 || channels[0].Model != model {
		t.Fatal("saved alias was changed", channels, err)
	}
	channel.Name = "edited gateway"
	updated, err := store.SaveChannel(ctx, "admin", channel)
	if err != nil || updated.Model != model || updated.Inference(DefaultSettings()).Model != model {
		t.Fatal("edited alias was changed", updated, err)
	}
}

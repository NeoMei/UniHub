package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unihub/internal/accounts"
	"unihub/internal/appintegrations"
	"unihub/internal/database"
	"unihub/internal/platformadmin"
	"unihub/internal/products"
	"unihub/internal/testutil"
)

func TestLegacyOIDCApplicationInventoryReservesDefaultInstance(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	require.NoError(t, database.Migrate(ctx, pool))
	user, err := accounts.New(pool).EnsureIdentity(ctx, accounts.Identity{Issuer: "https://issuer.example", Subject: "admin"})
	require.NoError(t, err)
	admin := platformadmin.New(pool, nil)
	require.NoError(t, admin.Bootstrap(ctx, "deployment-cli", user.ID, "inventory test"))

	c := config{
		PublicURL:      "https://hub.example",
		ProductClients: map[string]string{"legacy": "legacy-oidc", "dual": "bridge.other.default"},
		ProductAccess: map[string]products.Client{
			"dual-browser": {ClientID: "dual-browser", ProductID: "dual", BrowserSSO: true},
		},
	}
	store := newApplicationIntegrationStore(pool, admin, c)
	apps, err := store.List(ctx)
	require.NoError(t, err)
	require.Len(t, apps, 2)
	assert.Equal(t, "dual-browser", apps[0].ClientID)
	assert.Equal(t, "legacy-oidc", apps[1].ClientID)
	assert.Equal(t, "legacy", apps[1].ProductID)
	assert.Equal(t, "default", apps[1].InstanceID)
	assert.Equal(t, "deployment", apps[1].Source)
	assert.False(t, apps[1].BrowserSSO)
	assert.False(t, apps[1].RuntimeConfigured)

	input := appintegrations.Input{
		ProductID: "legacy", Name: "Legacy", Origin: "https://wiki.example",
		EntryURL: "https://wiki.example/start", CallbackURL: "https://wiki.example/callback",
		NativeLoginURL: "https://wiki.example/login",
	}
	_, err = store.Create(ctx, user.ID, input)
	require.ErrorIs(t, err, accounts.ErrConflict)

	input.ProductID = "other"
	_, err = store.Create(ctx, user.ID, input)
	require.ErrorIs(t, err, accounts.ErrConflict)

	input.ProductID, input.InstanceID = "legacy", "separate"
	app, err := store.Create(ctx, user.ID, input)
	require.NoError(t, err)
	assert.Equal(t, "managed", app.Source)
	assert.Equal(t, "separate", app.InstanceID)
}

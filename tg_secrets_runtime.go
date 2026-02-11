package main

//go:generate go run ./internal/tools/tgsecrets -in tg_secrets.local.json -out tg_secrets_generated.go

// embeddedTelegramCredentialsFunc is overridden by tg_secrets_generated.go (gitignored).
var embeddedTelegramCredentialsFunc = func() (int, string, bool) { return 0, "", false }

func embeddedTelegramCredentials() (int, string, bool) {
	return embeddedTelegramCredentialsFunc()
}

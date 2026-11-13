package twitch

// DefaultClientID — Client ID встроенного приложения Twitch (тип Public, регистрируется на dev.twitch.tv/console один раз для всего проекта).
// Client ID не секрет: он виден в любой ссылке авторизации. Секрета у Public-приложения нет, поэтому подключение идёт через
// Device Code Grant Flow: пользователю не нужно создавать своё приложение и вводить Client ID / Secret.
//
// Пустое значение — встроенного приложения нет, нужно заполнить своё (Settings → Twitch → «Use my own Twitch application»).
// Значение можно подставить и при сборке: -ldflags "-X github.com/compico/overlaycomposer/internal/twitch.DefaultClientID=...".
var DefaultClientID = "7mdk9f1o7q92fuy6cu7br5a3ntd0dh"

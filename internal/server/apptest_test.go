package server

import (
	"net/http"
	"time"

	"github.com/gofiber/fiber/v3"
)

// appTest — app.Test с запасом по времени: по умолчанию Fiber ждёт ответ 1 с, а под -race на медленном CI это бывает мало.
func appTest(app *fiber.App, req *http.Request) (*http.Response, error) {
	return app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
}

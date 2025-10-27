package routes

import (
	"naevis/discord"
	"naevis/middleware"
	"naevis/ratelim"
	"naevis/utils"
	"net/http"

	"github.com/julienschmidt/httprouter"
)

func AddDiscordRoutes(router *httprouter.Router, rateLimiter *ratelim.RateLimiter) {
	router.GET("/merechats/all", middleware.Authenticate(discord.GetUserChats))
	router.POST("/merechats/start", middleware.Authenticate(discord.StartNewChat))
	router.GET("/merechats/chat/:chatid", middleware.Authenticate(discord.GetChatByID))
	router.GET("/merechats/chat/:chatid/messages", middleware.Authenticate(discord.GetChatMessages))
	router.POST("/merechats/chat/:chatid/message", middleware.Authenticate(discord.SendMessageREST))
	router.PATCH("/merechats/messages/:messageid", middleware.Authenticate(discord.EditMessage))
	router.DELETE("/merechats/messages/:messageid", middleware.Authenticate(discord.DeleteMessage))

	// WebSocket also needs auth to ensure only valid users connect
	router.GET("/ws/merechat", middleware.Authenticate(func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		discord.HandleWebSocket(w, r, httprouter.Params{})
	}))

	router.POST("/merechats/chat/:chatid/upload", middleware.Authenticate(discord.UploadAttachment))
	router.GET("/merechats/chat/:chatid/search", middleware.Authenticate(discord.SearchMessages))
	router.GET("/merechats/messages/unread-count", middleware.Authenticate(discord.GetUnreadCount))
	router.POST("/merechats/messages/:messageid/read", middleware.Authenticate(discord.MarkAsRead))
}

func AddUtilityRoutes(router *httprouter.Router, rateLimiter *ratelim.RateLimiter) {
	router.GET("/csrf", rateLimiter.Limit(middleware.Authenticate(utils.CSRF)))
}

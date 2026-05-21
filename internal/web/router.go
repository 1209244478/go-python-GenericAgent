package web

import "github.com/gin-gonic/gin"

func SetupRouter(h *Handler) *gin.Engine {
	r := gin.Default()

	r.Use(CORSMiddleware())

	r.Static("/static", "./web")
	r.StaticFile("/", "./web/app.html")
	r.StaticFile("/login", "./web/login.html")

	api := r.Group("/api")
	{
		auth := api.Group("/auth")
		{
			auth.POST("/send-code", h.SendCode)
			auth.POST("/register", h.Register)
			auth.POST("/login", h.Login)
		}

		protected := api.Group("")
		protected.Use(AuthMiddleware(h.jwtMgr))
		{
			protected.GET("/user/profile", h.GetProfile)

			protected.POST("/agent/run", h.RunAgent)
			protected.POST("/agent/stream", h.StreamAgent)
			protected.GET("/agent/ws", h.WebSocketAgent)

			protected.GET("/chat/history", h.GetChatHistory)
			protected.DELETE("/chat/history", h.ClearChatHistory)

			protected.GET("/workspace/files", h.ListFiles)
			protected.GET("/workspace/file", h.ReadFile)
			protected.POST("/workspace/upload", h.UploadFile)
			protected.GET("/workspace/download", h.DownloadFile)
			protected.DELETE("/workspace/file", h.DeleteFile)

			protected.GET("/templates", h.ListTemplates)
			protected.GET("/skills", h.ListSkills)
		}
	}

	return r
}

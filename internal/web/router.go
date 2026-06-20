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

			// Task API (长任务能力)
			protected.POST("/agent/run-task", h.StartTask)
			protected.GET("/agent/stream-task/:taskId", h.StreamTask)
			protected.POST("/agent/abort-task/:taskId", h.AbortTask)
			protected.POST("/agent/resume-task/:taskId", h.ResumeTask)
			protected.GET("/agent/tasks", h.ListTasks)
			protected.GET("/agent/task/:taskId", h.GetTask)

			protected.GET("/chat/history", h.GetChatHistory)
			protected.DELETE("/chat/history", h.ClearChatHistory)

			protected.GET("/workspace/files", h.ListFiles)
			protected.GET("/workspace/file", h.ReadFile)
			protected.GET("/workspace/preview", h.PreviewFile)
			protected.POST("/workspace/upload", h.UploadFile)
			protected.POST("/workspace/save", h.SaveFile)
			protected.GET("/workspace/download", h.DownloadFile)
			protected.DELETE("/workspace/file", h.DeleteFile)

			protected.GET("/templates", h.ListTemplates)
			protected.GET("/skills", h.ListSkills)

			protected.GET("/sessions", h.ListSessions)
			protected.POST("/sessions", h.CreateSession)
			protected.DELETE("/sessions", h.DeleteSession)
		}
	}

	return r
}

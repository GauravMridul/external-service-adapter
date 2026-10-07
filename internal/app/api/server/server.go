package server

import (
	"esa/internal/app/constants"
	commoninit "esa/internal/app/init"
	"net/http"

	"github.com/gin-gonic/gin"
)

func RunServer(router *gin.Engine) {
	log := commoninit.GetLogger()

	port := commoninit.GetConfigString(constants.ServerPort, ":8080")
	log.Infow("Starting server", "port", port)

	// Check environment for gin mode
	env := commoninit.GetConfigString("environment", "dev")
	if env == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	if err := http.ListenAndServe(port, router); err != nil {
		log.Errorw("Server failed to start", "error", err)
		panic(err)
	}
}

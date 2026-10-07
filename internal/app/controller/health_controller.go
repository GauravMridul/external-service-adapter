package controller

import (
	"esa/internal/app/constants"
	"fmt"
	"net/http"
	"os"

	"esa/internal/app/shutdown"

	"github.com/gin-gonic/gin"
)

// Health Check Controller
type HealthController struct{}

func (h HealthController) Status(c *gin.Context) {

	version, err := os.ReadFile(constants.GetDeploymentVersionPath)
	if err != nil {
		fmt.Println("failed to read file deployedVersion")
	}
	c.JSON(http.StatusOK,
		gin.H{"Version": string(version)})
}

// Ready returns 200 when the pod can receive traffic, 503 when shutting down.
// Kubernetes readiness probe should use this endpoint; when it returns 503 the pod is removed from Service endpoints.
func (h HealthController) Ready(c *gin.Context) {
	if shutdown.IsShuttingDown() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "shutting_down"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}

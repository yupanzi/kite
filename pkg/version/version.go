package version

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/zxh326/kite/pkg/common"
)

var (
	Version   = "dev"
	BuildDate = "unknown"
	CommitID  = "unknown"
	// CommitURLBase is the web-URL prefix a commit id is appended to, so the UI can
	// deep-link the deployed commit. Defaults to the upstream GitHub repo; the fork's
	// release workflow overrides it via -X so the link resolves on the fork.
	CommitURLBase = "https://github.com/zxh326/kite/commit/"
)

type VersionInfo struct {
	Version   string `json:"version"`
	BuildDate string `json:"buildDate"`
	CommitID  string `json:"commitId"`
	CommitURL string `json:"commitUrl"`
	HasNew    bool   `json:"hasNewVersion"`
	Release   string `json:"releaseUrl"`
}

func GetVersion(c *gin.Context) {
	versionInfo := VersionInfo{
		Version:   Version,
		BuildDate: BuildDate,
		CommitID:  CommitID,
		CommitURL: CommitURLBase + CommitID,
	}

	if common.EnableVersionCheck {
		r := checkForUpdate(c.Request.Context(), Version)
		versionInfo.HasNew = r.hasNew
		if versionInfo.HasNew {
			versionInfo.Release = r.releaseURL
		}
	}
	c.JSON(http.StatusOK, versionInfo)
}

package handler

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/lp/campus-market/internal/constants"
	"github.com/lp/campus-market/internal/dto"
	"github.com/lp/campus-market/internal/middleware"
	"github.com/lp/campus-market/internal/service"
	"github.com/lp/campus-market/internal/util"
)

// AppealHandler exposes credit appeal endpoints for students and admins.
type AppealHandler struct {
	svc    *service.AppealService
	users  *service.UserService
	logger *slog.Logger
}

// NewAppealHandler wires the appeal handler dependencies.
func NewAppealHandler(svc *service.AppealService, users *service.UserService, logger *slog.Logger) *AppealHandler {
	return &AppealHandler{svc: svc, users: users, logger: logger}
}

// Create handles POST /appeals — a review receiver files an appeal.
func (h *AppealHandler) Create(c *gin.Context) {
	userID, err := middleware.CurrentUserID(c)
	if err != nil {
		util.Fail(c, http.StatusUnauthorized, constants.CodeUnauthorized, constants.MsgUnauthorized)
		return
	}
	user, err := h.users.GetProfile(c.Request.Context(), userID)
	if err != nil {
		c.Error(err)
		return
	}
	var req dto.CreateAppealRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.logger.Warn("appeal submit validation failed: user_id=" + strconv.FormatUint(uint64(userID), 10) + " error=" + err.Error())
		util.Fail(c, http.StatusBadRequest, constants.CodeValidation, constants.MsgValidationFailed)
		return
	}
	view, err := h.svc.Submit(c.Request.Context(), user, &req)
	if err != nil {
		c.Error(err)
		return
	}
	util.OK(c, view)
}

// ListMine handles GET /appeals/me — the appellant's appeals and progress.
func (h *AppealHandler) ListMine(c *gin.Context) {
	userID, err := middleware.CurrentUserID(c)
	if err != nil {
		util.Fail(c, http.StatusUnauthorized, constants.CodeUnauthorized, constants.MsgUnauthorized)
		return
	}
	views, err := h.svc.ListMine(c.Request.Context(), userID)
	if err != nil {
		c.Error(err)
		return
	}
	util.OK(c, views)
}

// GetProgress handles GET /appeals/:id — one appeal's progress for its owner.
func (h *AppealHandler) GetProgress(c *gin.Context) {
	userID, err := middleware.CurrentUserID(c)
	if err != nil {
		util.Fail(c, http.StatusUnauthorized, constants.CodeUnauthorized, constants.MsgUnauthorized)
		return
	}
	appealID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		util.Fail(c, http.StatusBadRequest, constants.CodeBadRequest, constants.MsgAppealNotFound)
		return
	}
	view, err := h.svc.GetProgress(c.Request.Context(), userID, uint(appealID))
	if err != nil {
		c.Error(err)
		return
	}
	util.OK(c, view)
}

// AdminList handles GET /admin/appeals — all appeals, optional ?status=.
func (h *AppealHandler) AdminList(c *gin.Context) {
	adminID, err := middleware.CurrentUserID(c)
	if err != nil {
		util.Fail(c, http.StatusUnauthorized, constants.CodeUnauthorized, constants.MsgUnauthorized)
		return
	}
	views, err := h.svc.AdminList(c.Request.Context(), c.Query("status"), adminID)
	if err != nil {
		c.Error(err)
		return
	}
	util.OK(c, views)
}

// AdminReview handles POST /admin/appeals/:id/review — approve or reject.
func (h *AppealHandler) AdminReview(c *gin.Context) {
	adminID, err := middleware.CurrentUserID(c)
	if err != nil {
		util.Fail(c, http.StatusUnauthorized, constants.CodeUnauthorized, constants.MsgUnauthorized)
		return
	}
	admin, err := h.users.GetProfile(c.Request.Context(), adminID)
	if err != nil {
		c.Error(err)
		return
	}
	appealID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		util.Fail(c, http.StatusBadRequest, constants.CodeBadRequest, constants.MsgAppealNotFound)
		return
	}
	var req dto.ReviewAppealRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.logger.Warn("appeal review validation failed: appeal_id=" + strconv.FormatUint(appealID, 10) + " error=" + err.Error())
		util.Fail(c, http.StatusBadRequest, constants.CodeValidation, constants.MsgValidationFailed)
		return
	}
	view, err := h.svc.Review(c.Request.Context(), admin, uint(appealID), &req)
	if err != nil {
		c.Error(err)
		return
	}
	util.OK(c, view)
}

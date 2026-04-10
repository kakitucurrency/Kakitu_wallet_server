package controller

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/render"
	"github.com/kakitucurrency/kakitu-wallet-server/reap"
	"github.com/kakitucurrency/kakitu-wallet-server/repository"
	"github.com/kakitucurrency/kakitu-wallet-server/utils"
	"k8s.io/klog/v2"
)

type IssuingController struct {
	CardRepo *repository.StripeCardRepo
}

type issueCardRequest struct {
	Account  string `json:"account"`
	Name     string `json:"name"`
	Phone    string `json:"phone"`
	DOB      string `json:"dob"`      // YYYY-MM-DD
	IDNumber string `json:"id_number"`
}

// HandleIssueCard handles POST /issuing/card
// NOTE: This endpoint does not verify that the caller owns the requested account.
// Caller authentication is a systemic concern for the entire API and not scoped to this handler.
func (ic *IssuingController) HandleIssueCard(w http.ResponseWriter, r *http.Request) {
	var req issueCardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ErrBadrequest(w, r, "invalid_request_body")
		return
	}
	if !utils.ValidateEthAddress(req.Account) {
		ErrBadrequest(w, r, "invalid_account")
		return
	}
	if req.Name == "" {
		ErrBadrequest(w, r, "name_required")
		return
	}
	if req.Phone == "" {
		ErrBadrequest(w, r, "phone_required")
		return
	}
	if req.DOB == "" {
		ErrBadrequest(w, r, "dob_required")
		return
	}
	if req.IDNumber == "" {
		ErrBadrequest(w, r, "id_number_required")
		return
	}

	// Return existing card if already issued
	existing, err := ic.CardRepo.GetCardByAccount(req.Account)
	if err != nil {
		klog.Errorf("HandleIssueCard: GetCardByAccount error: %v", err)
		ErrInternalServerError(w, r, "database_error")
		return
	}
	if existing != nil {
		render.Status(r, http.StatusOK)
		render.JSON(w, r, map[string]interface{}{
			"card_id": existing.StripeCardID,
			"status":  "active",
		})
		return
	}

	// Split name into first / last
	parts := strings.SplitN(strings.TrimSpace(req.Name), " ", 2)
	firstName := parts[0]
	lastName := ""
	if len(parts) > 1 {
		lastName = parts[1]
	}

	// Issue card via Reap
	issued, err := reap.CreateCard(reap.CardholderParams{
		FirstName: firstName,
		LastName:  lastName,
		Phone:     req.Phone,
		DOB:       req.DOB,
		IDNumber:  req.IDNumber,
		MetaID:    req.Account,
	})
	if err != nil {
		klog.Errorf("HandleIssueCard: Reap CreateCard error for %s: %v", req.Account, err)
		ErrInternalServerError(w, r, "card_issuance_error")
		return
	}

	// Persist
	if _, err := ic.CardRepo.CreateCard(req.Account, req.Account, issued.CardID); err != nil {
		klog.Errorf("HandleIssueCard: CreateCard DB error for %s: %v", req.Account, err)
		ErrInternalServerError(w, r, "database_error")
		return
	}

	render.Status(r, http.StatusOK)
	render.JSON(w, r, map[string]interface{}{
		"card_id": issued.CardID,
		"last4":   issued.Last4,
		"status":  "active",
	})
}

// HandleCardDetails handles GET /issuing/card/details?account=0x...
// Returns non-sensitive card info. Full PAN/CVV/expiry are served via Reap's iframe widget (coming soon).
// NOTE: This endpoint does not verify that the caller owns the requested account.
// Caller authentication is a systemic concern for the entire API and not scoped to this handler.
func (ic *IssuingController) HandleCardDetails(w http.ResponseWriter, r *http.Request) {
	account := r.URL.Query().Get("account")
	if !utils.ValidateEthAddress(account) {
		ErrBadrequest(w, r, "invalid_account")
		return
	}

	card, err := ic.CardRepo.GetCardByAccount(account)
	if err != nil {
		klog.Errorf("HandleCardDetails: GetCardByAccount error: %v", err)
		ErrInternalServerError(w, r, "database_error")
		return
	}
	if card == nil {
		render.Status(r, http.StatusNotFound)
		render.JSON(w, r, map[string]string{"error": "card_not_found"})
		return
	}

	info, err := reap.GetCard(card.StripeCardID)
	if err != nil {
		klog.Errorf("HandleCardDetails: Reap GetCard error: %v", err)
		ErrInternalServerError(w, r, "card_fetch_error")
		return
	}

	render.Status(r, http.StatusOK)
	render.JSON(w, r, map[string]interface{}{
		"card_id": card.StripeCardID,
		"last4":   info.Last4,
		"status":  info.Status,
	})
}

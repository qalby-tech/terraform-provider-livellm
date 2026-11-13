package client

import (
	"context"
	"net/http"
	"net/url"
)

// --- GPUs -------------------------------------------------------------------
//
// Renting a GPU (GET/POST /v1/gpus and its sub-routes). Renting spends money:
// these calls only send what they are given, and the confirm of a charged
// cancel is sent only when the caller passes one.

// GPUModel is one GPU model of the cloud (GET /v1/gpus/models): its price an
// hour (nil while it has none), its cards, how many are free now per use,
// how long a rental starting now could last per use (nil: no limit), and,
// with from and to, the free cards in each hour.
type GPUModel struct {
	Model     string             `json:"model"`
	Name      string             `json:"name"`
	CentsHour *int               `json:"centsHour,omitempty"`
	Total     int                `json:"total"`
	FreeNow   map[string]int     `json:"freeNow"`
	FreeUntil map[string]*string `json:"freeUntil"`
	Slots     []int              `json:"slots,omitempty"`
}

// GPULocation is where a cluster may go: mode auto, region or host.
type GPULocation struct {
	Mode   string `json:"mode,omitempty"`
	Region string `json:"region,omitempty"`
	Host   string `json:"host,omitempty"`
}

// GPUTarget is what a booking's GPUs go to at its start: a machine (machine
// use) or apps and Desktop Apps (apps use).
type GPUTarget struct {
	Machine string   `json:"machine,omitempty"`
	Apps    []string `json:"apps,omitempty"`
}

// GPUCard is one GPU of a rental (a cluster of one or more).
type GPUCard struct {
	ID        string `json:"id"`
	Model     string `json:"model"`
	Name      string `json:"name"`
	State     string `json:"state"`
	Answering bool   `json:"answering"`
}

// GPURental is one rental (a cluster of one or more GPUs) as GET /v1/gpus
// lists it.
type GPURental struct {
	ID             string       `json:"id"`
	Model          string       `json:"model"`
	Name           string       `json:"name"`
	Use            string       `json:"use"`
	State          string       `json:"state"`
	StartsAt       string       `json:"startsAt"`
	EndsAt         *string      `json:"endsAt"`
	StartedAt      *string      `json:"startedAt"`
	EndedAt        *string      `json:"endedAt"`
	EndedReason    *string      `json:"endedReason"`
	Booked         bool         `json:"booked"`
	NoEnd          bool         `json:"noEnd"`
	Phase          string       `json:"phase"`
	UsedBy         []string     `json:"usedBy"`
	Releasing      bool         `json:"releasing"`
	RestartHint    bool         `json:"restartHint"`
	StopsAt        *string      `json:"stopsAt"`
	CanChangeUseAt *string      `json:"canChangeUseAt"`
	HoursThisMonth float64      `json:"hoursThisMonth"`
	CentsThisMonth *int64       `json:"centsThisMonth"`
	Cards          []GPUCard    `json:"cards"`
	Location       *GPULocation `json:"location"`
	Host           *string      `json:"host"`
	Region         *string      `json:"region"`
}

// Over says whether the rental is over for good: ended or cancelled.
func (r GPURental) Over() bool { return r.State == "ended" || r.State == "cancelled" }

// GPUClusterAsk is one cluster asked for: its use, a count per model (all on
// one server), where it may go and a booking's target.
type GPUClusterAsk struct {
	Use      string         `json:"use"`
	Cards    map[string]int `json:"cards"`
	Location *GPULocation   `json:"location,omitempty"`
	Target   *GPUTarget     `json:"target,omitempty"`
}

// GPURentRequest is POST /v1/gpus's body: Model and Use for one GPU, or
// Clusters; StartsAt makes it a booking.
type GPURentRequest struct {
	Model    string          `json:"model,omitempty"`
	Use      string          `json:"use,omitempty"`
	Location *GPULocation    `json:"location,omitempty"`
	Clusters []GPUClusterAsk `json:"clusters,omitempty"`
	StartsAt string          `json:"startsAt,omitempty"`
	EndsAt   string          `json:"endsAt,omitempty"`
	Target   *GPUTarget      `json:"target,omitempty"`
}

// GPUModels lists the models; use, from and to are sent when set.
func (c *Client) GPUModels(ctx context.Context, use, from, to string) ([]GPUModel, error) {
	q := url.Values{}
	for k, v := range map[string]string{"use": use, "from": from, "to": to} {
		if v != "" {
			q.Set(k, v)
		}
	}
	path := "/v1/gpus/models"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out struct {
		Models []GPUModel `json:"models"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Models, nil
}

// GPURentals lists the workspace's GPUs: booked, waiting, running, ended in
// the last 30 days.
func (c *Client) GPURentals(ctx context.Context) ([]GPURental, error) {
	var out struct {
		Rentals []GPURental `json:"rentals"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/gpus", nil, &out); err != nil {
		return nil, err
	}
	return out.Rentals, nil
}

// GPURental finds one rental by id in the list; nil when it isn't listed.
func (c *Client) GPURental(ctx context.Context, id string) (*GPURental, error) {
	rs, err := c.GPURentals(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rs {
		if rs[i].ID == id {
			return &rs[i], nil
		}
	}
	return nil, nil
}

// RentGPU rents (or books) and answers the id of each rental made: one for
// one GPU, one per cluster for clusters.
func (c *Client) RentGPU(ctx context.Context, body GPURentRequest) ([]string, error) {
	var out struct {
		Rental   *GPURental  `json:"rental"`
		Clusters []GPURental `json:"clusters"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/gpus", body, &out); err != nil {
		return nil, err
	}
	var ids []string
	if out.Rental != nil && out.Rental.ID != "" {
		ids = append(ids, out.Rental.ID)
	}
	for _, r := range out.Clusters {
		ids = append(ids, r.ID)
	}
	return ids, nil
}

// ExtendGPU gives a rental a later end (whole UTC hours).
func (c *Client) ExtendGPU(ctx context.Context, id, endsAt string) error {
	return c.do(ctx, http.MethodPatch, "/v1/gpus/"+url.PathEscape(id), map[string]any{"endsAt": endsAt}, nil)
}

// ChangeGPUUse changes a rental's use and answers whether its GPUs are still
// getting ready for it (202).
func (c *Client) ChangeGPUUse(ctx context.Context, id, use string) (switching bool, err error) {
	status, err := c.doStatus(ctx, http.MethodPost, "/v1/gpus/"+url.PathEscape(id)+"/use", map[string]any{"use": use}, nil)
	return status == http.StatusAccepted, err
}

// ReleaseGPU releases a rental or cancels a booking and answers what it is
// charged in all (US cents). confirm is sent only when set: a booking that
// starts within a day is charged in full and answers 428 without it.
func (c *Client) ReleaseGPU(ctx context.Context, id, confirm string) (int64, error) {
	var in any
	if confirm != "" {
		in = map[string]any{"confirm": confirm}
	}
	var out struct {
		ChargedCents int64 `json:"chargedCents"`
	}
	err := c.do(ctx, http.MethodDelete, "/v1/gpus/"+url.PathEscape(id), in, &out)
	return out.ChargedCents, err
}

// RemoveGPUCard takes one GPU off a cluster of several. confirm is sent only
// when set (a booked GPU within a day of its start: 428 without it).
func (c *Client) RemoveGPUCard(ctx context.Context, id, card, confirm string) error {
	var in any
	if confirm != "" {
		in = map[string]any{"confirm": confirm}
	}
	return c.do(ctx, http.MethodDelete, "/v1/gpus/"+url.PathEscape(id)+"/cards/"+url.PathEscape(card), in, nil)
}

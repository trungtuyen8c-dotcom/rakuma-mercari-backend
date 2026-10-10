// Package models holds the JSON shapes shared by the API, the frontend and the MCP server.
// IDs are sent as strings; dates as "YYYY-MM-DD" ("" when missing); money as integer yen.
package models

type Product struct {
	ID       int64  `json:"id,string"`
	Name     string `json:"name"`
	Active   bool   `json:"active"`
	TxCount  int    `json:"txCount"`
	Keywords string `json:"keywords"` // comma-separated words that identify it in a Rakuma item title
}

type Period struct {
	ID             int64   `json:"id,string"`
	Label          string  `json:"label"`
	Start          string  `json:"start"`
	End            string  `json:"end"`
	Status         string  `json:"status"`
	OpeningCost    int64   `json:"openingCost"`
	OpeningRevenue int64   `json:"openingRevenue"`
	AdjustCost     int64   `json:"adjustCost"`    // owner's correction to the period's total cost
	AdjustRevenue  int64   `json:"adjustRevenue"` // and to its total revenue
	ClosingCost    *int64  `json:"closingCost"`
	ClosingRevenue *int64  `json:"closingRevenue"`
	ClosedAt       *string `json:"closedAt"`
}

type Purchase struct {
	ID          int64  `json:"id,string"`
	PeriodID    int64  `json:"periodId,string"`
	PeriodLabel string `json:"periodLabel"`
	STT         int    `json:"stt"`
	Source      string `json:"source"`
	Date        string `json:"date"`
	Link        string `json:"link"`
	ProductID   int64  `json:"productId,string"`
	ProductName string `json:"productName"`
	Price       int64  `json:"price"`
	Qty         int    `json:"qty"`
	Discount    int64  `json:"discount"`
	Total       int64  `json:"total"`
	Tracking    string `json:"tracking"`
	Merged      bool   `json:"merged"`
	Checked     bool   `json:"checked"`
	Reviewed    bool   `json:"reviewed"`
	Note        string `json:"note"`
	DupLink     bool   `json:"dupLink"`
	DupTracking bool   `json:"dupTracking"`
	Locked      bool   `json:"locked"`
}

type Sale struct {
	ID          int64  `json:"id,string"`
	PeriodID    int64  `json:"periodId,string"`
	PeriodLabel string `json:"periodLabel"`
	STT         int    `json:"stt"`
	Date        string `json:"date"`
	ProductID   int64  `json:"productId,string"`
	ProductName string `json:"productName"`
	Qty         int    `json:"qty"`
	Price       int64  `json:"price"`
	Ship        int64  `json:"ship"`
	Total       int64  `json:"total"`
	Customer    string `json:"customer"`
	Note        string `json:"note"`
	Locked      bool   `json:"locked"`
}

type StockRow struct {
	STT       int    `json:"stt"`
	ProductID int64  `json:"productId,string"`
	Name      string `json:"name"`
	Active    bool   `json:"active"`
	Opening   int    `json:"opening"`
	Incoming  int    `json:"incoming"`
	Sold      int    `json:"sold"`
	Adjust    int    `json:"adjust"` // owner's stock correction in this period
	Current   int    `json:"current"`
}

type Totals struct {
	PrevCost      int64 `json:"prevCost"`
	PrevRevenue   int64 `json:"prevRevenue"`
	PrevProfit    int64 `json:"prevProfit"`
	PeriodCost    int64 `json:"periodCost"`
	PeriodRevenue int64 `json:"periodRevenue"`
	PeriodProfit  int64 `json:"periodProfit"`
	TotalCost     int64 `json:"totalCost"`
	TotalRevenue  int64 `json:"totalRevenue"`
	TotalProfit   int64 `json:"totalProfit"`
	SalesCount    int   `json:"salesCount"`
	StockTotal    int   `json:"stockTotal"`
}

type Dashboard struct {
	Period    Period     `json:"period"`
	Totals    Totals     `json:"totals"`
	Negatives []StockRow `json:"negatives"`
	Matches   bool       `json:"matches"` // closed period: recomputed totals equal the stored closing figures
}

type Settings struct {
	Rate       float64 `json:"rate"`
	ServiceFee int64   `json:"serviceFee"`
	Shipping   int64   `json:"shipping"`
	Tax        float64 `json:"tax"`
	OwnerEmail string  `json:"ownerEmail"`
}

type APIKey struct {
	ID        int64   `json:"id,string"`
	Name      string  `json:"name"`
	Scope     string  `json:"scope"`
	Masked    string  `json:"masked"`
	CreatedAt string  `json:"createdAt"`
	LastUsed  *string `json:"lastUsed"`
	Revoked   bool    `json:"revoked"`
	RevokedAt *string `json:"revokedAt"`
}

type User struct {
	Email      string `json:"email"`
	Name       string `json:"name"`
	SignedInAt string `json:"signedInAt"`
}

type AnalysisLot struct {
	PurchaseID  int64  `json:"purchaseId,string"`
	Date        string `json:"date"`
	PeriodLabel string `json:"periodLabel"`
	STT         int    `json:"stt"`
	UnitCost    int64  `json:"unitCost"`
	Qty         int    `json:"qty"`
	Take        int    `json:"take"`
	Cost        int64  `json:"cost"`
}

type Analysis struct {
	ProductID   int64         `json:"productId,string"`
	ProductName string        `json:"productName"`
	SoldQty     int           `json:"soldQty"`
	SoldAmount  int64         `json:"soldAmount"`
	InQty       int           `json:"inQty"`
	InAmount    int64         `json:"inAmount"`
	COGS        int64         `json:"cogs"`
	Profit      int64         `json:"profit"`
	Uncovered   int           `json:"uncovered"` // sold units with no purchase to cost them
	Lots        []AnalysisLot `json:"lots"`
}

// State is everything the web UI needs in one request.
type State struct {
	Products  []Product                 `json:"products"`
	Purchases []Purchase                `json:"purchases"`
	Sales     []Sale                    `json:"sales"`
	Periods   []Period                  `json:"periods"`
	Openings  map[string]map[string]int `json:"openings"`
	Adjusts   map[string]map[string]int `json:"stockAdjusts"`
	APIKeys   []APIKey                  `json:"apiKeys"`
	Settings  Settings                  `json:"settings"`
	User      *User                     `json:"user"`
	Rakuma    []RakumaOrder             `json:"rakuma"`
}

type RakumaMessage struct {
	ID   int64  `json:"id,string"`
	From string `json:"from"` // seller | buyer
	At   string `json:"at"`   // Rakuma's display text
	Body string `json:"body"`
	New  bool   `json:"new"` // arrived after the owner last marked the thread handled
}

// RakumaOrder is one order synced from Rakuma. Price and Discount are order totals, not per unit.
type RakumaOrder struct {
	ID          int64           `json:"id,string"`
	OrderNo     string          `json:"orderNo"`
	Link        string          `json:"link"`
	Title       string          `json:"title"`
	Image       string          `json:"image"`
	Status      string          `json:"status"`
	Date        string          `json:"date"`
	Price       int64           `json:"price"`
	Discount    int64           `json:"discount"`
	Carrier     string          `json:"carrier"`
	Tracking    string          `json:"tracking"`
	Seller      string          `json:"seller"`
	Summary     string          `json:"summary"`
	ReplyDraft  string          `json:"replyDraft"`
	Rating      string          `json:"rating"`    // "" | GOOD | NORMAL | BAD
	IssueNote   string          `json:"issueNote"` // non-empty = open problem, shown highlighted
	PurchaseID  *string         `json:"purchaseId"`
	Dismissed   bool            `json:"dismissed"`
	ChatOpen    bool            `json:"chatOpen"`
	NewMessages int             `json:"newMessages"`
	Messages    []RakumaMessage `json:"messages"`
	Replies     []RakumaReply   `json:"replies"`
	SyncedAt    string          `json:"syncedAt"`
	// MissingSince: when an unfinished order stopped appearing on Rakuma ("" = listed); the owner should check it
	MissingSince string `json:"missingSince"`
}

type RakumaReply struct {
	ID        int64  `json:"id,string"`
	BodyVi    string `json:"bodyVi"`
	BodyJa    string `json:"bodyJa"`
	Kind      string `json:"kind"`   // REPLY | BROADCAST
	Status    string `json:"status"` // PENDING | SENT | SKIPPED
	Reason    string `json:"reason"` // why it was skipped
	CreatedAt string `json:"createdAt"`
	SentAt    string `json:"sentAt"`
}

package appie

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// orderDetailsResponse matches the API response for order details grouped by taxonomy.
type orderDetailsResponse struct {
	OrderID      int    `json:"orderId"`
	OrderState   string `json:"orderState"`
	DeliveryDate string `json:"deliveryDate"`
	DeliveryType string `json:"deliveryType"`
	DeliveryTime struct {
		StartDateTime string `json:"startDateTime"`
		EndDateTime   string `json:"endDateTime"`
	} `json:"deliveryTimePeriod"`
	GroupedProducts []struct {
		TaxonomyName    string `json:"taxonomyName"`
		OrderedProducts []struct {
			Amount   int `json:"amount"`
			Quantity int `json:"quantity"`
			Product  struct {
				WebshopID        int      `json:"webshopId"`
				Title            string   `json:"title"`
				Brand            string   `json:"brand"`
				SalesUnitSize    string   `json:"salesUnitSize"`
				PriceBeforeBonus float64  `json:"priceBeforeBonus"`
				CurrentPrice     *float64 `json:"currentPrice"` // discounted price, nil when discount is per-group (e.g. "2e gratis")
				IsBonus          bool     `json:"isBonus"`
				BonusMechanism   string   `json:"bonusMechanism"`
			} `json:"product"`
		} `json:"orderedProducts"`
	} `json:"groupedProductsInTaxonomy"`
}

func (r *orderDetailsResponse) toOrder() Order {
	var items []OrderItem
	for _, group := range r.GroupedProducts {
		for _, op := range group.OrderedProducts {
			price := Price{Now: op.Product.PriceBeforeBonus}
			if op.Product.CurrentPrice != nil {
				price = Price{Now: *op.Product.CurrentPrice, Was: op.Product.PriceBeforeBonus}
			}
			items = append(items, OrderItem{
				ProductID: op.Product.WebshopID,
				Quantity:  op.Quantity,
				Product: &Product{
					ID:             op.Product.WebshopID,
					Title:          op.Product.Title,
					Brand:          op.Product.Brand,
					UnitSize:       op.Product.SalesUnitSize,
					Price:          price,
					IsBonus:        op.Product.IsBonus,
					BonusMechanism: op.Product.BonusMechanism,
				},
			})
		}
	}

	return Order{
		ID:         strconv.Itoa(r.OrderID),
		State:      r.OrderState,
		Items:      items,
		TotalCount: len(items),
	}
}

// orderSummaryResponse matches the API response for active order summary.
type orderSummaryResponse struct {
	ID           int    `json:"id"`
	State        string `json:"state"`
	ShoppingType string `json:"shoppingType"`
	TotalPrice   struct {
		PriceBeforeDiscount float64 `json:"priceBeforeDiscount"`
		PriceAfterDiscount  float64 `json:"priceAfterDiscount"`
		PriceDiscount       float64 `json:"priceDiscount"`
		PriceTotalPayable   float64 `json:"priceTotalPayable"`
	} `json:"totalPrice"`
	DeliveryInformation struct {
		DeliveryDate      string `json:"deliveryDate"`
		DeliveryStartTime string `json:"deliveryStartTime"`
		DeliveryEndTime   string `json:"deliveryEndTime"`
		Address           struct {
			Street      string `json:"street"`
			HouseNumber int    `json:"houseNumber"`
			ZipCode     int    `json:"zipCode"`
			City        string `json:"city"`
		} `json:"address"`
	} `json:"deliveryInformation"`
	OrderedProducts []struct {
		Amount   int `json:"amount"`
		Quantity int `json:"quantity"`
		Product  struct {
			WebshopID int    `json:"webshopId"`
			Title     string `json:"title"`
			Brand     string `json:"brand"`
			Images    []struct {
				URL string `json:"url"`
			} `json:"images"`
		} `json:"product"`
	} `json:"orderedProducts"`
}

func (r *orderSummaryResponse) toOrder() Order {
	items := make([]OrderItem, 0, len(r.OrderedProducts))
	for _, op := range r.OrderedProducts {
		var images []Image
		for _, img := range op.Product.Images {
			images = append(images, Image{URL: img.URL})
		}
		items = append(items, OrderItem{
			ProductID: op.Product.WebshopID,
			Quantity:  op.Quantity,
			Product: &Product{
				ID:     op.Product.WebshopID,
				Title:  op.Product.Title,
				Brand:  op.Product.Brand,
				Images: images,
			},
		})
	}

	return Order{
		ID:            strconv.Itoa(r.ID),
		State:         r.State,
		Items:         items,
		TotalCount:    len(items),
		TotalPrice:    r.TotalPrice.PriceTotalPayable,
		TotalDiscount: r.TotalPrice.PriceDiscount,
	}
}

// GetOrder retrieves the current active order (shopping cart).
// The order ID is cached for use in subsequent order operations.
func (c *Client) GetOrder(ctx context.Context) (*Order, error) {
	var result orderSummaryResponse
	if err := c.DoRequest(ctx, http.MethodGet, "/mobile-services/order/v1/summaries/active?sortBy=DEFAULT", nil, &result); err != nil {
		return nil, fmt.Errorf("get order failed: %w", err)
	}

	// Store order ID for subsequent requests
	c.mu.Lock()
	c.orderID = strconv.Itoa(result.ID)
	c.mu.Unlock()

	order := result.toOrder()
	return &order, nil
}

// GetOrderDetails retrieves the details of a specific order by ID.
// Unlike GetOrder, this works for any order (not just the active one) and
// returns products grouped by taxonomy (category).
func (c *Client) GetOrderDetails(ctx context.Context, orderID int) (*Order, error) {
	path := fmt.Sprintf("/mobile-services/order/v1/%d/details-grouped-by-taxonomy", orderID)

	var result orderDetailsResponse
	if err := c.DoRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, fmt.Errorf("get order details failed: %w", err)
	}

	order := result.toOrder()
	return &order, nil
}

// SetOrderID manually sets the order ID for subsequent API requests.
// This is useful when selecting a specific order from fulfillments.
func (c *Client) SetOrderID(id int) {
	c.mu.Lock()
	c.orderID = strconv.Itoa(id)
	c.mu.Unlock()
}

// AddToOrder adds or updates items in the current order.
// If an item already exists, its quantity is updated. Set quantity to 0 to remove.
//
// Example:
//
//	err := client.AddToOrder(ctx, []appie.OrderItem{
//	    {ProductID: 123456, Quantity: 2},
//	})
func (c *Client) AddToOrder(ctx context.Context, items []OrderItem) error {
	type itemRequest struct {
		ProductID     int    `json:"productId"`
		Quantity      int    `json:"quantity"`
		OriginCode    string `json:"originCode"`
		Description   string `json:"description"`
		Strikethrough bool   `json:"strikethrough"`
	}

	// Merge duplicates: the API rejects requests with duplicate product IDs.
	merged := make(map[int]int, len(items))
	for _, item := range items {
		merged[item.ProductID] += item.Quantity
	}

	reqItems := make([]itemRequest, 0, len(merged))
	for pid, qty := range merged {
		reqItems = append(reqItems, itemRequest{
			ProductID:     pid,
			Quantity:      qty,
			OriginCode:    "PRD",
			Description:   "",
			Strikethrough: false,
		})
	}

	body := map[string]any{
		"items": reqItems,
	}

	if err := c.DoRequest(ctx, http.MethodPut, "/mobile-services/order/v1/items?sortBy=DEFAULT", body, nil); err != nil {
		return fmt.Errorf("add to order failed: %w", err)
	}

	return nil
}

// RemoveFromOrder removes an item from the current order by setting quantity to 0.
func (c *Client) RemoveFromOrder(ctx context.Context, productID int) error {
	return c.AddToOrder(ctx, []OrderItem{{ProductID: productID, Quantity: 0}})
}

// UpdateOrderItem updates the quantity of an item in the order.
func (c *Client) UpdateOrderItem(ctx context.Context, productID, quantity int) error {
	return c.AddToOrder(ctx, []OrderItem{{ProductID: productID, Quantity: quantity}})
}

// ClearOrder removes all items from the current order.
func (c *Client) ClearOrder(ctx context.Context) error {
	order, err := c.GetOrder(ctx)
	if err != nil {
		return err
	}

	if len(order.Items) == 0 {
		return nil
	}

	removals := make([]OrderItem, 0, len(order.Items))
	for _, item := range order.Items {
		removals = append(removals, OrderItem{ProductID: item.ProductID, Quantity: 0})
	}

	return c.AddToOrder(ctx, removals)
}

// GetOrderSummary retrieves the order summary/totals.
func (c *Client) GetOrderSummary(ctx context.Context) (*OrderSummary, error) {
	var result orderSummaryResponse
	if err := c.DoRequest(ctx, http.MethodGet, "/mobile-services/order/v1/summaries/active?sortBy=DEFAULT", nil, &result); err != nil {
		return nil, fmt.Errorf("get order summary failed: %w", err)
	}

	return &OrderSummary{
		TotalItems:    len(result.OrderedProducts),
		TotalPrice:    result.TotalPrice.PriceTotalPayable,
		TotalDiscount: result.TotalPrice.PriceDiscount,
	}, nil
}

const fetchMyListBasketQuery = `query FetchMyListBasket($input: BasketInput, $states: [BonusSegmentState!]) {
  basket(input: $input) {
    id
    canChangeDelivery
    itemsInOrder {
      originCode
      quantity
      allocatedQuantity
      isClosed
      product {
        id
        title
        brand
        salesUnitSize
        priceV2(states: $states) {
          now { amount }
          was { amount }
          discount { description }
        }
      }
    }
    products {
      originCode
      quantity
      product {
        id
        title
        brand
        salesUnitSize
        priceV2(states: $states) {
          now { amount }
          was { amount }
          discount { description }
        }
      }
    }
    summary {
      quantity
      price {
        totalPrice { amount }
        discount { amount }
      }
    }
  }
}`

// GetMyListBasket retrieves the current basket via GraphQL FetchMyListBasket.
// This query returns product-level details and basket totals in one request.
func (c *Client) GetMyListBasket(ctx context.Context) (*Order, error) {
	type priceV2 struct {
		Now struct {
			Amount float64 `json:"amount"`
		} `json:"now"`
		Was *struct {
			Amount float64 `json:"amount"`
		} `json:"was"`
		Discount *struct {
			Description string `json:"description"`
		} `json:"discount"`
	}
	type basketProduct struct {
		ID            int     `json:"id"`
		Title         string  `json:"title"`
		Brand         string  `json:"brand"`
		SalesUnitSize string  `json:"salesUnitSize"`
		PriceV2       priceV2 `json:"priceV2"`
	}
	type basketItem struct {
		OriginCode        string        `json:"originCode"`
		Quantity          int           `json:"quantity"`
		AllocatedQuantity int           `json:"allocatedQuantity"`
		IsClosed          bool          `json:"isClosed"`
		Product           basketProduct `json:"product"`
	}
	type basketResponse struct {
		Basket *struct {
			ID           string       `json:"id"`
			ItemsInOrder []basketItem `json:"itemsInOrder"`
			Products     []basketItem `json:"products"`
			Summary      struct {
				Quantity int `json:"quantity"`
				Price    struct {
					TotalPrice struct {
						Amount float64 `json:"amount"`
					} `json:"totalPrice"`
					Discount struct {
						Amount float64 `json:"amount"`
					} `json:"discount"`
				} `json:"price"`
			} `json:"summary"`
		} `json:"basket"`
	}

	var resp basketResponse
	vars := map[string]any{
		"input": nil,
		"states": []string{
			"ACTIVATED", "ASSIGNED", "NONE", "REDEEMABLE",
		},
	}
	if err := c.DoGraphQL(ctx, fetchMyListBasketQuery, vars, &resp); err != nil {
		return nil, fmt.Errorf("get basket failed: %w", err)
	}

	if resp.Basket == nil || resp.Basket.ID == "" {
		return nil, fmt.Errorf("no active basket")
	}

	rawItems := resp.Basket.ItemsInOrder
	if len(rawItems) == 0 {
		rawItems = resp.Basket.Products
	}

	items := make([]OrderItem, 0, len(rawItems))
	for _, it := range rawItems {
		p := it.Product
		price := Price{Now: p.PriceV2.Now.Amount}
		if p.PriceV2.Was != nil && p.PriceV2.Was.Amount > 0 {
			price.Was = p.PriceV2.Was.Amount
		}

		bonus := ""
		if p.PriceV2.Discount != nil {
			bonus = p.PriceV2.Discount.Description
		}

		items = append(items, OrderItem{
			ProductID: p.ID,
			Quantity:  it.Quantity,
			Product: &Product{
				ID:             p.ID,
				Title:          p.Title,
				Brand:          p.Brand,
				UnitSize:       p.SalesUnitSize,
				Price:          price,
				BonusMechanism: bonus,
			},
		})
	}

	return &Order{
		ID:            resp.Basket.ID,
		Items:         items,
		TotalCount:    len(items),
		TotalPrice:    resp.Basket.Summary.Price.TotalPrice.Amount,
		TotalDiscount: resp.Basket.Summary.Price.Discount.Amount,
	}, nil
}

// GetCheckoutInfo retrieves checkout preflight metadata for an order.
// This includes counts for recommendation and validation buckets such as
// missing bonus products and non-deliverables.
func (c *Client) GetCheckoutInfo(ctx context.Context, orderID int) (*CheckoutInfo, error) {
	path := fmt.Sprintf("/mobile-services/order/v1/%d/checkout", orderID)

	var raw struct {
		KassaKoopjes        []json.RawMessage `json:"kassaKoopjes"`
		MissingBonus        []json.RawMessage `json:"missingBonus"`
		NonChosen           []json.RawMessage `json:"nonChosen"`
		NonDeliverables     []json.RawMessage `json:"nonDeliverables"`
		RecommendedProducts []json.RawMessage `json:"recommendedProducts"`
		Samples             []json.RawMessage `json:"samples"`
		ShowMakeCompleet    bool              `json:"showMakeCompleet"`
	}
	if err := c.DoRequest(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return nil, fmt.Errorf("get checkout info failed: %w", err)
	}

	return &CheckoutInfo{
		KassaKoopjes:        len(raw.KassaKoopjes),
		MissingBonus:        len(raw.MissingBonus),
		NonChosen:           len(raw.NonChosen),
		NonDeliverables:     len(raw.NonDeliverables),
		RecommendedProducts: len(raw.RecommendedProducts),
		Samples:             len(raw.Samples),
		ShowMakeCompleet:    raw.ShowMakeCompleet,
	}, nil
}

// UpdateOrderState updates the server-side state for an order.
// The API expects a plain-text body (for example: "RESET" or "SUBMIT").
func (c *Client) UpdateOrderState(ctx context.Context, orderID int, state string) error {
	state = strings.TrimSpace(strings.ToUpper(state))
	if state == "" {
		return fmt.Errorf("state cannot be empty")
	}

	path := fmt.Sprintf("/mobile-services/order/v1/%d/state?orderBy=DEFAULT", orderID)
	if err := c.doPlainTextRequest(ctx, http.MethodPut, path, state); err != nil {
		return fmt.Errorf("update order state failed: %w", err)
	}
	return nil
}

// SubmitOrder attempts to submit the order using the state transition API.
func (c *Client) SubmitOrder(ctx context.Context, orderID int) error {
	return c.UpdateOrderState(ctx, orderID, "SUBMIT")
}

func (c *Client) doPlainTextRequest(ctx context.Context, method, path, body string) error {
	c.ensureFreshToken(ctx, path)

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "text/plain")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr apiError
		if json.Unmarshal(respBody, &apiErr) == nil && (apiErr.Code != "" || apiErr.Message != "") {
			return &apiErr
		}
		return fmt.Errorf("API error: %d %s", resp.StatusCode, string(respBody))
	}

	return nil
}

const reopenOrderMutation = `mutation OrderReopen($id: Int!) {
  orderReopen(id: $id) {
    status
    errorMessage
  }
}`

// ReopenOrder reopens a submitted order so items can be added or modified.
// This is required before calling AddToOrder on a fulfillment-selected order.
func (c *Client) ReopenOrder(ctx context.Context, orderID int) error {
	type reopenResponse struct {
		OrderReopen struct {
			Status       string `json:"status"`
			ErrorMessage string `json:"errorMessage"`
		} `json:"orderReopen"`
	}

	var resp reopenResponse
	vars := map[string]any{"id": orderID}
	if err := c.DoGraphQL(ctx, reopenOrderMutation, vars, &resp); err != nil {
		return fmt.Errorf("reopen order failed: %w", err)
	}

	if resp.OrderReopen.Status != "SUCCESS" {
		return fmt.Errorf("reopen order failed: %s", resp.OrderReopen.ErrorMessage)
	}

	return nil
}

const revertOrderMutation = `mutation OrderRevert($id: Int!) {
  orderRevert(id: $id) {
    status
    errorMessage
  }
}`

// RevertOrder reverts a reopened order back to its submitted state.
// Use this after ReopenOrder to deactivate the order as the active one.
func (c *Client) RevertOrder(ctx context.Context, orderID int) error {
	type revertResponse struct {
		OrderRevert struct {
			Status       string `json:"status"`
			ErrorMessage string `json:"errorMessage"`
		} `json:"orderRevert"`
	}

	var resp revertResponse
	vars := map[string]any{"id": orderID}
	if err := c.DoGraphQL(ctx, revertOrderMutation, vars, &resp); err != nil {
		return fmt.Errorf("revert order failed: %w", err)
	}

	if resp.OrderRevert.Status != "SUCCESS" {
		return fmt.Errorf("revert order failed: %s", resp.OrderRevert.ErrorMessage)
	}

	// Clear client-side order state
	c.mu.Lock()
	c.orderID = ""
	c.orderHash = ""
	c.mu.Unlock()

	return nil
}

const fulfillmentsQuery = `query OrderFulfillments {
  orderFulfillments(status: OPEN) {
    result {
      orderId
      statusCode
      statusDescription
      shoppingType
      transactionCompleted
      modifiable
      totalPrice {
        totalPrice { amount }
      }
      delivery {
        status
        method
        slot {
          date
          dateDisplay
          timeDisplay
          startTime
          endTime
        }
        address {
          street
          houseNumber
          houseNumberExtra
          city
          postalCode
        }
      }
    }
  }
}`

type fulfillmentsResponse struct {
	OrderFulfillments struct {
		Result []fulfillmentResult `json:"result"`
	} `json:"orderFulfillments"`
}

type fulfillmentResult struct {
	OrderID              int    `json:"orderId"`
	StatusCode           int    `json:"statusCode"`
	StatusDescription    string `json:"statusDescription"`
	ShoppingType         string `json:"shoppingType"`
	TransactionCompleted bool   `json:"transactionCompleted"`
	Modifiable           bool   `json:"modifiable"`
	TotalPrice           struct {
		TotalPrice struct {
			Amount float64 `json:"amount"`
		} `json:"totalPrice"`
	} `json:"totalPrice"`
	Delivery FulfillmentDelivery `json:"delivery"`
}

// GetFulfillments retrieves all open (scheduled) order fulfillments.
// These are orders that have been submitted and are awaiting delivery.
func (c *Client) GetFulfillments(ctx context.Context) ([]Fulfillment, error) {
	var resp fulfillmentsResponse
	if err := c.DoGraphQL(ctx, fulfillmentsQuery, nil, &resp); err != nil {
		return nil, fmt.Errorf("get fulfillments failed: %w", err)
	}

	results := resp.OrderFulfillments.Result
	fulfillments := make([]Fulfillment, 0, len(results))

	for _, r := range results {
		fulfillments = append(fulfillments, Fulfillment{
			OrderID:              r.OrderID,
			Status:               r.Delivery.Status,
			StatusDescription:    r.StatusDescription,
			ShoppingType:         r.ShoppingType,
			TotalPrice:           r.TotalPrice.TotalPrice.Amount,
			TransactionCompleted: r.TransactionCompleted,
			Modifiable:           r.Modifiable,
			Delivery:             r.Delivery,
		})
	}

	return fulfillments, nil
}

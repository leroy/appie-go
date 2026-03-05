package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	appie "github.com/gwillem/appie-go"
)

type basketCommand struct {
	Show     basketShowCommand     `command:"show" description:"Show active basket"`
	Add      basketAddCommand      `command:"add" description:"Add a product to active basket"`
	Rm       basketRmCommand       `command:"rm" description:"Remove a product from active basket"`
	Clear    basketClearCommand    `command:"clear" description:"Clear active basket"`
	Checkout basketCheckoutCommand `command:"checkout" description:"Show checkout info and optionally submit"`
}

func (cmd *basketCommand) Execute(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("unknown argument %q, did you mean: appie basket show", args[0])
	}
	return cmd.Show.Execute(nil)
}

func getActiveBasket(ctx context.Context, client *appie.Client) (*appie.Order, int, error) {
	order, err := client.GetMyListBasket(ctx)
	if err == nil {
		orderID, err := strconv.Atoi(order.ID)
		if err == nil {
			return order, orderID, nil
		}

		// Fall back to active order summary for numeric order ID extraction.
		summary, sErr := client.GetOrder(ctx)
		if sErr != nil {
			return nil, 0, fmt.Errorf("failed to resolve basket order id: %w", sErr)
		}
		orderID, sErr = strconv.Atoi(summary.ID)
		if sErr != nil {
			return nil, 0, fmt.Errorf("invalid active order id %q: %w", summary.ID, sErr)
		}
		return order, orderID, nil
	}
	if strings.Contains(err.Error(), "no active basket") || strings.Contains(err.Error(), "Order does not exist") {
		return nil, 0, fmt.Errorf("no active basket; run 'appie order' and then 'appie order reopen <order-id>'")
	}

	// Compatibility fallback: if GraphQL basket fails for other reasons, use
	// the existing REST-based basket retrieval path.
	summary, err := client.GetOrder(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "Order does not exist") {
			return nil, 0, fmt.Errorf("no active basket; run 'appie order' and then 'appie order reopen <order-id>'")
		}
		return nil, 0, fmt.Errorf("failed to get active basket: %w", err)
	}

	orderID, err := strconv.Atoi(summary.ID)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid active order id %q: %w", summary.ID, err)
	}

	// Enrich active basket with detailed product info when available.
	order, err = client.GetOrderDetails(ctx, orderID)
	if err != nil {
		order = summary
	} else {
		order.TotalPrice = summary.TotalPrice
		order.TotalDiscount = summary.TotalDiscount
	}

	return order, orderID, nil
}

type basketShowCommand struct{}

func (cmd *basketShowCommand) Execute(args []string) error {
	ctx, client, err := orderSetup()
	if err != nil {
		return err
	}

	order, _, err := getActiveBasket(ctx, client)
	if err != nil {
		return err
	}
	return printOrder(order, nil)
}

type basketAddCommand struct {
	Args struct {
		Product string `positional-arg-name:"product" required:"true"`
	} `positional-args:"yes"`
	Quantity int `short:"n" long:"quantity" default:"1" description:"Quantity to add"`
}

func (cmd *basketAddCommand) Execute(args []string) error {
	ctx, client, err := orderSetup()
	if err != nil {
		return err
	}

	_, orderID, err := getActiveBasket(ctx, client)
	if err != nil {
		return err
	}

	productID, err := strconv.Atoi(cmd.Args.Product)
	if err != nil {
		products, err := client.SearchProducts(ctx, cmd.Args.Product, 15)
		if err != nil {
			return fmt.Errorf("search failed: %w", err)
		}
		if len(products) == 0 {
			return fmt.Errorf("no products found for %q", cmd.Args.Product)
		}
		if len(products) > 1 {
			printProducts(products)
			return fmt.Errorf("multiple matches for %q, specify product ID", cmd.Args.Product)
		}
		productID = products[0].ID
		fmt.Printf("Found: %s\n", products[0].Title)
	}

	if err := client.AddToOrder(ctx, []appie.OrderItem{{ProductID: productID, Quantity: cmd.Quantity}}); err != nil {
		return err
	}

	fmt.Printf("Added %dx %d to basket %d\n", cmd.Quantity, productID, orderID)
	return nil
}

type basketRmCommand struct {
	Args struct {
		ProductID int `positional-arg-name:"product-id" required:"true"`
	} `positional-args:"yes"`
}

func (cmd *basketRmCommand) Execute(args []string) error {
	ctx, client, err := orderSetup()
	if err != nil {
		return err
	}

	_, orderID, err := getActiveBasket(ctx, client)
	if err != nil {
		return err
	}

	if err := client.RemoveFromOrder(ctx, cmd.Args.ProductID); err != nil {
		return err
	}
	fmt.Printf("Removed %d from basket %d\n", cmd.Args.ProductID, orderID)
	return nil
}

type basketClearCommand struct{}

func (cmd *basketClearCommand) Execute(args []string) error {
	ctx, client, err := orderSetup()
	if err != nil {
		return err
	}

	order, orderID, err := getActiveBasket(ctx, client)
	if err != nil {
		return err
	}
	if len(order.Items) == 0 {
		fmt.Println("Basket is already empty")
		return nil
	}

	if err := client.ClearOrder(ctx); err != nil {
		return err
	}
	fmt.Printf("Cleared basket %d\n", orderID)
	return nil
}

type basketCheckoutCommand struct {
	Submit bool   `long:"submit" description:"Submit order after checkout preflight"`
	State  string `long:"state" default:"SUBMIT" description:"Order state transition used for submit"`
}

func (cmd *basketCheckoutCommand) Execute(args []string) error {
	ctx, client, err := orderSetup()
	if err != nil {
		return err
	}

	order, orderID, err := getActiveBasket(ctx, client)
	if err != nil {
		return err
	}

	checkout, err := client.GetCheckoutInfo(ctx, orderID)
	if err != nil {
		return err
	}

	fmt.Printf("Checkout for basket %d\n", orderID)
	fmt.Printf("Total:          €%.2f\n", order.TotalPrice)
	fmt.Printf("Items:          %d\n", len(order.Items))
	fmt.Printf("Missing bonus:  %d\n", checkout.MissingBonus)
	fmt.Printf("Non-chosen:     %d\n", checkout.NonChosen)
	fmt.Printf("Non-deliverables: %d\n", checkout.NonDeliverables)
	fmt.Printf("Kassa koopjes:  %d\n", checkout.KassaKoopjes)
	fmt.Printf("Recommended:    %d\n", checkout.RecommendedProducts)
	fmt.Printf("Samples:        %d\n", checkout.Samples)

	if !cmd.Submit {
		fmt.Println()
		fmt.Println("Run 'appie basket checkout --submit' to submit this basket.")
		return nil
	}

	state := strings.TrimSpace(cmd.State)
	if state == "" {
		return fmt.Errorf("submit state cannot be empty")
	}

	if err := client.UpdateOrderState(ctx, orderID, state); err != nil {
		return err
	}

	fmt.Printf("Submitted basket %d using state %s\n", orderID, strings.ToUpper(state))
	return nil
}

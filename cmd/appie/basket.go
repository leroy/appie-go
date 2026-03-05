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
	order, orderID, err := client.GetMyListBasketWithOrderID(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "no active basket") {
			return nil, 0, fmt.Errorf("no basket found")
		}
		return nil, 0, err
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

	if err := client.AddToBasket(ctx, productID, cmd.Quantity); err != nil {
		return err
	}

	if orderID > 0 {
		fmt.Printf("Added %dx %d to basket (order %d)\n", cmd.Quantity, productID, orderID)
	} else {
		fmt.Printf("Added %dx %d to basket\n", cmd.Quantity, productID)
	}
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

	if err := client.RemoveFromBasket(ctx, cmd.Args.ProductID); err != nil {
		return err
	}
	if orderID > 0 {
		fmt.Printf("Removed %d from basket (order %d)\n", cmd.Args.ProductID, orderID)
	} else {
		fmt.Printf("Removed %d from basket\n", cmd.Args.ProductID)
	}
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

	if err := client.ClearBasket(ctx, order.ID); err != nil {
		return err
	}
	if orderID > 0 {
		fmt.Printf("Cleared basket (order %d)\n", orderID)
	} else {
		fmt.Println("Cleared basket")
	}
	return nil
}

type basketCheckoutCommand struct {
	Submit      bool   `long:"submit" description:"Submit order after checkout preflight"`
	State       string `long:"state" default:"SUBMIT" description:"Order state transition used for submit"`
	Limit       int    `short:"n" long:"limit" default:"20" description:"Max delivery slot lines to show"`
	SelectDate  string `long:"select-date" description:"Select delivery date (YYYY-MM-DD) and create/link order"`
	SelectShift string `long:"select-shift" description:"Select shift code from listed slots (requires --select-date)"`
}

func chooseDeliverySlot(days []appie.DeliverySlotDayOption, date, shiftCode string) (*appie.DeliverySlotOption, error) {
	for _, day := range days {
		if day.Date != date {
			continue
		}
		for i := range day.Slots {
			slot := &day.Slots[i]
			if slot.ShiftCode == shiftCode {
				return slot, nil
			}
		}
	}
	return nil, fmt.Errorf("slot not found for date %s and shift %s", date, shiftCode)
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

	fmt.Printf("Checkout for basket %s\n", order.ID)
	fmt.Printf("Total:          €%.2f\n", order.TotalPrice)
	fmt.Printf("Items:          %d\n", len(order.Items))

	member, err := client.GetMember(ctx)
	if err != nil {
		return fmt.Errorf("failed to get member profile for delivery slots: %w", err)
	}
	slots, err := client.GetOrderDeliverySlots(ctx, member.Address)
	if err != nil {
		return fmt.Errorf("failed to get delivery slots: %w", err)
	}

	fmt.Println()
	fmt.Println("Available delivery slots:")
	shown := 0
	for _, day := range slots {
		for _, slot := range day.Slots {
			if slot.IsFullyBooked {
				continue
			}
			fmt.Printf("  %s  %s-%s  €%.2f  shift=%s  loc=%d\n", day.Date, slot.StartTime, slot.EndTime, slot.Price, slot.ShiftCode, slot.DeliveryLocationID)
			shown++
			if shown >= cmd.Limit {
				break
			}
		}
		if shown >= cmd.Limit {
			break
		}
	}
	if shown == 0 {
		fmt.Println("  No available slots found")
	}

	wantsSelect := cmd.SelectDate != "" || cmd.SelectShift != ""
	if wantsSelect {
		if cmd.SelectDate == "" || cmd.SelectShift == "" {
			return fmt.Errorf("slot selection requires both --select-date and --select-shift")
		}
		slot, err := chooseDeliverySlot(slots, cmd.SelectDate, cmd.SelectShift)
		if err != nil {
			return err
		}
		if slot.IsFullyBooked {
			return fmt.Errorf("selected slot %s %s is fully booked", cmd.SelectDate, cmd.SelectShift)
		}
		newOrderID, err := client.CheckinOrderSlot(ctx, *slot, member.Address)
		if err != nil {
			return err
		}
		orderID = newOrderID
		fmt.Printf("\nSelected slot %s %s-%s (shift=%s)\n", slot.Date, slot.StartTime, slot.EndTime, slot.ShiftCode)
	}

	if orderID > 0 {
		fmt.Printf("\nActive order: %d\n", orderID)
	} else {
		fmt.Println("\nNo active order is linked to this basket yet.")
	}

	if !cmd.Submit {
		if orderID <= 0 {
			fmt.Println("Use --select-date and --select-shift to check in a slot and create/link an order.")
		} else {
			fmt.Println("Run 'appie basket checkout --submit' to submit this basket.")
		}
		return nil
	}
	if orderID <= 0 {
		return fmt.Errorf("cannot submit: no active order linked to this basket")
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

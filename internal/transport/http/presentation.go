package http

import (
	"fmt"
	"strings"

	"github.com/eightyun/stablecoin-gateway/internal/chain/tron"
	"github.com/eightyun/stablecoin-gateway/internal/deposit"
	"github.com/eightyun/stablecoin-gateway/internal/payout"
)

func presentDepositIntent(details deposit.IntentDetails) (deposit.IntentDetails, error) {
	contract, err := presentChainAddress(details.Network, details.ContractAddress)
	if err != nil {
		return deposit.IntentDetails{}, fmt.Errorf("转换充值合约地址: %w", err)
	}
	address, err := presentChainAddress(details.Network, details.DepositAddress)
	if err != nil {
		return deposit.IntentDetails{}, fmt.Errorf("转换充值地址: %w", err)
	}
	details.ContractAddress = contract
	details.DepositAddress = address
	return details, nil
}

func presentBalances(balances []deposit.Balance) ([]deposit.Balance, error) {
	result := make([]deposit.Balance, len(balances))
	for index, balance := range balances {
		contract, err := presentChainAddress(balance.Network, balance.ContractAddress)
		if err != nil {
			return nil, fmt.Errorf("转换余额合约地址: %w", err)
		}
		balance.ContractAddress = contract
		result[index] = balance
	}
	return result, nil
}

func presentPayout(details payout.Details) (payout.Details, error) {
	contract, err := presentChainAddress(details.Network, details.ContractAddress)
	if err != nil {
		return payout.Details{}, fmt.Errorf("转换出款合约地址: %w", err)
	}
	destination, err := presentChainAddress(details.Network, details.DestinationAddress)
	if err != nil {
		return payout.Details{}, fmt.Errorf("转换出款目的地址: %w", err)
	}
	details.ContractAddress = contract
	details.DestinationAddress = destination
	return details, nil
}

func presentChainAddress(network, address string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(network)), "tron-") {
		return address, nil
	}
	return tron.NormalizeAddressBase58(address)
}

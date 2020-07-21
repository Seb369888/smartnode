package minipool

import (
    "bytes"
    "fmt"
    "math/big"

    "github.com/ethereum/go-ethereum/common"
    "github.com/rocket-pool/rocketpool-go/minipool"
    "github.com/rocket-pool/rocketpool-go/rocketpool"
    "github.com/rocket-pool/rocketpool-go/tokens"
    "github.com/rocket-pool/rocketpool-go/types"
    "golang.org/x/sync/errgroup"
)


// Minipool details
type minipoolDetails struct {
    Address common.Address
    ValidatorPubkey types.ValidatorPubkey
    Status minipool.StatusDetails
    DepositType types.MinipoolDeposit
    Node minipool.NodeDetails
    NethBalance *big.Int
    User minipool.UserDetails
    Staking minipool.StakingDetails
}


// Validate that a minipool belongs to a node
func validateMinipoolOwner(mp *minipool.Minipool, nodeAddress common.Address) error {
    owner, err := mp.GetNodeAddress()
    if err != nil {
        return err
    }
    if !bytes.Equal(owner.Bytes(), nodeAddress.Bytes()) {
        return fmt.Errorf("Minipool %s does not belong to the node", mp.Address.Hex())
    }
    return nil
}


// Get all node minipool details
func getNodeMinipoolDetails(rp *rocketpool.RocketPool, nodeAddress common.Address) ([]minipoolDetails, error) {

    // Get minipool addresses
    addresses, err := minipool.GetNodeMinipoolAddresses(rp, nodeAddress)
    if err != nil {
        return []minipoolDetails{}, err
    }

    // Data
    var wg errgroup.Group
    details := make([]minipoolDetails, len(addresses))

    // Load details
    for mi, address := range addresses {
        mi, address := mi, address
        wg.Go(func() error {
            mpDetails, err := getMinipoolDetails(rp, address)
            if err == nil { details[mi] = mpDetails }
            return err
        })
    }

    // Wait for data
    if err := wg.Wait(); err != nil {
        return []minipoolDetails{}, err
    }

    // Return
    return details, nil

}


// Get a minipool's details
func getMinipoolDetails(rp *rocketpool.RocketPool, minipoolAddress common.Address) (minipoolDetails, error) {

    // Create minipool
    mp, err := minipool.NewMinipool(rp, minipoolAddress)
    if err != nil {
        return minipoolDetails{}, err
    }

    // Data
    var wg errgroup.Group
    details := minipoolDetails{Address: minipoolAddress}

    // Load data
    wg.Go(func() error {
        var err error
        details.ValidatorPubkey, err = minipool.GetMinipoolPubkey(rp, minipoolAddress)
        return err
    })
    wg.Go(func() error {
        var err error
        details.Status, err = mp.GetStatusDetails()
        return err
    })
    wg.Go(func() error {
        var err error
        details.DepositType, err = mp.GetDepositType()
        return err
    })
    wg.Go(func() error {
        var err error
        details.Node, err = mp.GetNodeDetails()
        return err
    })
    wg.Go(func() error {
        var err error
        details.NethBalance, err = tokens.GetNETHBalance(rp, minipoolAddress)
        return err
    })
    wg.Go(func() error {
        var err error
        details.User, err = mp.GetUserDetails()
        return err
    })
    wg.Go(func() error {
        var err error
        details.Staking, err = mp.GetStakingDetails()
        return err
    })

    // Wait for data
    if err := wg.Wait(); err != nil {
        return minipoolDetails{}, err
    }

    // Return
    return details, nil

}

package node

import (
	"fmt"
	"io/ioutil"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/rocket-pool/rocketpool-go/utils/eth"
	"github.com/urfave/cli"

	"github.com/rocket-pool/smartnode/shared/services/config"
	"github.com/rocket-pool/smartnode/shared/services/gas"
	"github.com/rocket-pool/smartnode/shared/services/rocketpool"
	"github.com/rocket-pool/smartnode/shared/types/api"
	cliutils "github.com/rocket-pool/smartnode/shared/utils/cli"
	"github.com/rocket-pool/smartnode/shared/utils/math"
)

const (
	colorBlue            string = "\033[36m"
	primaryFileGateway   string = "https://%s.ipfs.dweb.link/rp-rewards-%s-%d.json"
	secondaryFileGateway string = "https://ipfs.io/ipfs/%s/rp-rewards-%s-%d.json"
)

func nodeClaimRewards(c *cli.Context) error {

	// Get RP client
	rp, err := rocketpool.NewClientFromCtx(c)
	if err != nil {
		return err
	}
	defer rp.Close()

	// Check and assign the EC status
	err = cliutils.CheckExecutionClientStatus(rp)
	if err != nil {
		return err
	}

	// Check if we're using the legacy system or the new one
	updateStatusResponse, err := rp.MergeUpdateStatus()
	if err != nil {
		return fmt.Errorf("error checking if the merge updates have been deployed: %w", err)
	}

	if updateStatusResponse.IsUpdateDeployed {
		// Handle the new system
		return nodeClaimRewardsModern(c, rp)
	} else {
		// Handle the old system
		return nodeClaimRewardsLegacy(c, rp)
	}

}

func nodeClaimRewardsModern(c *cli.Context, rp *rocketpool.Client) error {

	// Get eligible intervals
	rewardsInfoResponse, err := rp.GetRewardsInfo()
	if err != nil {
		return fmt.Errorf("error getting rewards info: %w", err)
	}
	if len(rewardsInfoResponse.UnclaimedIntervals) == 0 {
		fmt.Println("Your node does not have any unclaimed rewards yet.")
		return nil
	}

	// Provide a notice
	fmt.Printf("%sWelcome to the new rewards system!\nYou no longer need to claim rewards at each interval - you can simply let them accumulate and claim them whenever you want.\nHere you can see which intervals you haven't claimed yet, and how many rewards you earned during each one.%s\n\n", colorBlue, colorReset)

	// Check for missing Merkle trees with rewards available
	missingIntervals := []uint64{}
	missingCIDs := []string{}
	for _, intervalInfo := range rewardsInfoResponse.UnclaimedIntervals {
		if !intervalInfo.TreeFileExists {
			fmt.Printf("You have rewards for interval %d but are missing the rewards tree file.\n", intervalInfo.Index)
			missingIntervals = append(missingIntervals, intervalInfo.Index)
			missingCIDs = append(missingCIDs, intervalInfo.CID)
		}
	}

	// Download the Merkle trees for all unclaimed intervals that don't exist
	if len(missingIntervals) > 0 {
		fmt.Println()
		fmt.Printf("%sNOTE: If you would like to regenerate these tree files manually, please answer `n` to the prompt below and run `rocketpool network generate-rewards-tree` before claiming your rewards.%s\n", colorBlue, colorReset)
		if !cliutils.Confirm("Would you like to download all missing rewards tree files now?") {
			fmt.Println("Cancelled.")
			return nil
		}

		// Load the config file
		cfg, _, err := rp.LoadConfig()
		if err != nil {
			return fmt.Errorf("error loading config: %w", err)
		}
		if !downloadRewardsFiles(cfg, missingIntervals, missingCIDs) {
			return nil
		}

		// Reload rewards now that the files are in place
		rewardsInfoResponse, err = rp.GetRewardsInfo()
		if err != nil {
			return fmt.Errorf("error getting rewards info: %w", err)
		}
	}

	// Print the info for all available periods
	totalRpl := big.NewInt(0)
	totalEth := big.NewInt(0)
	for _, intervalInfo := range rewardsInfoResponse.UnclaimedIntervals {
		fmt.Printf("Rewards for Interval %d:\n", intervalInfo.Index)
		fmt.Printf("\tStaking:        %.6f RPL\n", eth.WeiToEth(intervalInfo.CollateralRplAmount))
		fmt.Printf("\tOracle DAO:     %.6f RPL\n", eth.WeiToEth(intervalInfo.ODaoRplAmount))
		fmt.Printf("\tSmoothing Pool: %.6f ETH\n\n", eth.WeiToEth(intervalInfo.SmoothingPoolEthAmount))

		totalRpl.Add(totalRpl, intervalInfo.CollateralRplAmount)
		totalRpl.Add(totalRpl, intervalInfo.ODaoRplAmount)
		totalEth.Add(totalEth, intervalInfo.SmoothingPoolEthAmount)
	}

	fmt.Println("Total Pending Rewards:")
	fmt.Printf("\t%.6f RPL\n", eth.WeiToEth(totalRpl))
	fmt.Printf("\t%.6f ETH\n\n", eth.WeiToEth(totalEth))

	// Get the list of intervals to claim
	var indices []uint64
	validIndices := []string{}
	for _, intervalInfo := range rewardsInfoResponse.UnclaimedIntervals {
		validIndices = append(validIndices, fmt.Sprint(intervalInfo.Index))
	}
	for {
		indexSelection := ""
		if !c.Bool("yes") {
			indexSelection = cliutils.Prompt("Which intervals would you like to claim? Use a comma separated list (such as '1,2,3') or leave it blank to claim all intervals at once.", "^$|^\\d+(,\\d+)*$", "Invalid index selection")
		}

		indices = []uint64{}
		if indexSelection == "" {
			for _, intervalInfo := range rewardsInfoResponse.UnclaimedIntervals {
				indices = append(indices, intervalInfo.Index)
			}
			break
		} else {
			elements := strings.Split(indexSelection, ",")
			allValid := true
			seenIndices := map[uint64]bool{}

			for _, element := range elements {
				found := false
				for _, validIndex := range validIndices {
					if validIndex == element {
						found = true
						break
					}
				}
				if !found {
					fmt.Printf("'%s' is an invalid index.\nValid indices are: %s\n", element, strings.Join(validIndices, ","))
					allValid = false
					break
				}
				index, err := strconv.ParseUint(element, 0, 64)
				if err != nil {
					fmt.Printf("'%s' is an invalid index.\nValid indices are: %s\n", element, strings.Join(validIndices, ","))
					allValid = false
					break
				}

				// Ignore duplicates
				_, exists := seenIndices[index]
				if !exists {
					indices = append(indices, index)
					seenIndices[index] = true
				}
			}
			if allValid {
				break
			}
		}
	}

	// Calculate amount to be claimed
	claimRpl := big.NewInt(0)
	claimEth := big.NewInt(0)
	for _, intervalInfo := range rewardsInfoResponse.UnclaimedIntervals {
		for _, index := range indices {
			if intervalInfo.Index == index {
				claimRpl.Add(claimRpl, intervalInfo.CollateralRplAmount)
				claimRpl.Add(claimRpl, intervalInfo.ODaoRplAmount)
				claimEth.Add(claimEth, intervalInfo.SmoothingPoolEthAmount)
			}
		}
	}
	fmt.Printf("With this selection, you will claim %.6f RPL and %.6f ETH.\n\n", eth.WeiToEth(claimRpl), eth.WeiToEth(claimEth))

	// Get restake amount
	restakeAmountWei, err := getRestakeAmount(c, rewardsInfoResponse, claimRpl)
	if err != nil {
		return err
	}

	// Check claim ability
	if restakeAmountWei == nil {
		canClaim, err := rp.CanNodeClaimRewards(indices)
		if err != nil {
			return err
		}

		// Assign max fees
		err = gas.AssignMaxFeeAndLimit(canClaim.GasInfo, rp, c.Bool("yes"))
		if err != nil {
			return err
		}
	} else {
		canClaim, err := rp.CanNodeClaimAndStakeRewards(indices, restakeAmountWei)
		if err != nil {
			return err
		}

		// Assign max fees
		err = gas.AssignMaxFeeAndLimit(canClaim.GasInfo, rp, c.Bool("yes"))
		if err != nil {
			return err
		}
	}

	// Prompt for confirmation
	if !(c.Bool("yes") || cliutils.Confirm("Are you sure you want to claim your rewards?")) {
		fmt.Println("Cancelled.")
		return nil
	}

	// Claim rewards
	var txHash common.Hash
	if restakeAmountWei == nil {
		response, err := rp.NodeClaimRewards(indices)
		if err != nil {
			return err
		}
		txHash = response.TxHash
	} else {
		response, err := rp.NodeClaimAndStakeRewards(indices, restakeAmountWei)
		if err != nil {
			return err
		}
		txHash = response.TxHash
	}

	fmt.Printf("Claiming Rewards...\n")
	cliutils.PrintTransactionHash(rp, txHash)
	if _, err = rp.WaitForTransaction(txHash); err != nil {
		return err
	}

	// Log & return
	fmt.Println("Successfully claimed rewards.")
	return nil

}

// Determine how much RPL to restake
func getRestakeAmount(c *cli.Context, rewardsInfoResponse api.NodeGetRewardsInfoResponse, claimRpl *big.Int) (*big.Int, error) {

	// Get the current collateral
	currentCollateral := float64(0)
	rplToMaxCollateral := float64(0)
	rplPrice := eth.WeiToEth(rewardsInfoResponse.RplPrice)
	currentRplStake := eth.WeiToEth(rewardsInfoResponse.RplStake)
	activeMinipools := float64(rewardsInfoResponse.ActiveMinipools)
	availableRpl := eth.WeiToEth(claimRpl)

	// Print info about autostaking RPL
	var bestTotal float64
	var bestCollateral float64
	if rewardsInfoResponse.ActiveMinipools > 0 {
		currentCollateral = rplPrice * currentRplStake / (activeMinipools * 16.0)
		maxRplRequired := activeMinipools * 16.0 * 1.5 / rplPrice // NOTE: Assumes the max is 150%
		rplToMaxCollateral = maxRplRequired - currentRplStake

		fmt.Printf("You currently have %.6f RPL staked (%.2f%% collateral).\n", currentRplStake, currentCollateral)
		if rplToMaxCollateral <= 0 {
			fmt.Println("You are already at maximum collateral. Restaking more RPL will not lead to more rewards.")
		} else if availableRpl < rplToMaxCollateral {
			bestTotal = availableRpl + currentRplStake
			bestCollateral = rplPrice * bestTotal / (activeMinipools * 16.0)
			fmt.Printf("You can restake a max of %.6f RPL which will bring you to a total of %.6f RPL staked (%.2f%% collateral).\n", availableRpl, bestTotal, bestCollateral)
		} else {
			total := rplToMaxCollateral + currentRplStake
			fmt.Printf("If you restake %.6f RPL, you will have a total of %.6f RPL staked (the max collateral of 150%%).\nRestaking more than this will not result in higher rewards.\n\n", rplToMaxCollateral, total)
		}
	} else {
		fmt.Println("You do not have any active minipools, so restaking RPL will not lead to any rewards.")
	}

	// Handle restaking automation or prompts
	var restakeAmountWei *big.Int
	restakeAmountFlag := c.String("restake-amount")

	if restakeAmountFlag == "150%" {
		// Figure out how much to stake to get to 150% or the max available to claim, whichever is smaller
		if rplToMaxCollateral <= 0 {
			fmt.Println("Ignoring automatic staking request since your collateral is already maximized.")
			restakeAmountWei = nil
		} else if availableRpl < rplToMaxCollateral {
			fmt.Printf("Automatically restaking all of the claimable RPL, which will bring you to a total of %.6f RPL staked (%.2f%% collateral).\n", bestTotal, bestCollateral)
			restakeAmountWei = claimRpl
		} else {
			total := rplToMaxCollateral + currentRplStake
			fmt.Printf("Automatically restaking %.6f RPL, which will bring you to a total of %.6f RPL staked (150%% collateral).\n", rplToMaxCollateral, total)
			restakeAmountWei = eth.EthToWei(rplToMaxCollateral)
		}
	} else if restakeAmountFlag == "all" {
		// Restake everything with no regard for collateral level
		total := availableRpl + currentRplStake
		totalCollateral := rplPrice * total / (activeMinipools * 16.0)
		fmt.Printf("Automatically restaking all of the claimable RPL, which will bring you to a total of %.6f RPL staked (%.2f%% collateral).\n", total, totalCollateral)
		restakeAmountWei = claimRpl
	} else if restakeAmountFlag != "" {
		// Restake a specific amount, capped at how much is available to claim
		stakeAmount, err := strconv.ParseFloat(restakeAmountFlag, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid restake amount '%s': %w", restakeAmountFlag, err)
		}
		if availableRpl < stakeAmount {
			fmt.Printf("Limiting the automatic restake to all of the claimable RPL, which will bring you to a total of %.6f RPL staked (%.2f%% collateral).\n", bestTotal, bestCollateral)
			restakeAmountWei = claimRpl
		} else {
			total := stakeAmount + currentRplStake
			totalCollateral := rplPrice * total / (activeMinipools * 16.0)
			fmt.Printf("Automatically restaking %.6f RPL, which will bring you to a total of %.6f RPL staked (%.2f%% collateral).\n", stakeAmount, total, totalCollateral)
			restakeAmountWei = eth.EthToWei(stakeAmount)
		}
	} else if c.Bool("yes") {
		// Ignore automatic restaking if `-y` is specified but `-a` isn't
		fmt.Println("Automatic restaking is not requested.")
		restakeAmountWei = nil
	} else {
		// Prompt the user
		if rplToMaxCollateral <= 0 || availableRpl < rplToMaxCollateral {
			amountOptions := []string{
				"None (do not restake any RPL)",
				fmt.Sprintf("All %.6f RPL, which will bring you to %.2f%% collateral", availableRpl, bestCollateral*100),
				"A custom amount",
			}
			selected, _ := cliutils.Select("Please choose an amount to restake here:", amountOptions)
			switch selected {
			case 0:
				restakeAmountWei = nil
			case 1:
				restakeAmountWei = claimRpl
			case 2:
				for {
					inputAmount := cliutils.Prompt("Please enter an amount of RPL to stake:", "^\\d+(\\.\\d+)?$", "Invalid amount")
					stakeAmount, err := strconv.ParseFloat(inputAmount, 64)
					if err != nil {
						fmt.Printf("Invalid stake amount '%s': %s\n", inputAmount, err.Error())
					} else if stakeAmount < 0 {
						fmt.Println("Amount must be greater than zero.")
					} else if stakeAmount > availableRpl {
						fmt.Println("Amount must be less than the RPL available to claim.")
					} else {
						restakeAmountWei = eth.EthToWei(stakeAmount)
						break
					}
				}
			}
		} else {
			bestTotal = availableRpl + currentRplStake
			bestCollateral = rplPrice * bestTotal / (activeMinipools * 16.0)
			amountOptions := []string{
				"None (do not restake any RPL)",
				fmt.Sprintf("Enough to get to 150%% collateral (%.6f RPL)", rplToMaxCollateral),
				fmt.Sprintf("All %.6f RPL, which will bring you to %.2f%% collateral", availableRpl, bestCollateral*100),
				"A custom amount",
			}
			selected, _ := cliutils.Select("Please choose an amount to restake here:", amountOptions)
			switch selected {
			case 0:
				restakeAmountWei = nil
			case 1:
				restakeAmountWei = eth.EthToWei(rplToMaxCollateral)
			case 2:
				restakeAmountWei = claimRpl
			case 3:
				for {
					inputAmount := cliutils.Prompt("Please enter an amount of RPL to stake:", "^\\d+(\\.\\d+)?$", "Invalid amount")
					stakeAmount, err := strconv.ParseFloat(inputAmount, 64)
					if err != nil {
						fmt.Printf("Invalid stake amount '%s': %s\n", inputAmount, err.Error())
					} else if stakeAmount < 0 {
						fmt.Println("Amount must be greater than zero.")
					} else if stakeAmount > availableRpl {
						fmt.Println("Amount must be less than the RPL available to claim.")
					} else {
						restakeAmountWei = eth.EthToWei(stakeAmount)
						break
					}
				}
			}
		}
	}

	return restakeAmountWei, nil

}

func nodeClaimRewardsLegacy(c *cli.Context, rp *rocketpool.Client) error {

	// Provide a notice
	fmt.Println("NOTE: The merge contract update has not occurred yet, using the old RPL rewards system.\n")

	// Check for rewards
	canClaim, err := rp.CanNodeClaimRpl()
	if err != nil {
		return err
	}
	if canClaim.RplAmount.Cmp(big.NewInt(0)) == 0 {
		fmt.Println("The node does not have any available RPL rewards to claim.")
		return nil
	} else {
		fmt.Printf("%.6f RPL is available to claim.\n", math.RoundDown(eth.WeiToEth(canClaim.RplAmount), 6))
	}

	// Assign max fees
	err = gas.AssignMaxFeeAndLimit(canClaim.GasInfo, rp, c.Bool("yes"))
	if err != nil {
		return err
	}

	// Prompt for confirmation
	if !(c.Bool("yes") || cliutils.Confirm("Are you sure you want to claim your RPL?")) {
		fmt.Println("Cancelled.")
		return nil
	}

	// Claim rewards
	response, err := rp.NodeClaimRpl()
	if err != nil {
		return err
	}

	fmt.Printf("Claiming RPL...\n")
	cliutils.PrintTransactionHash(rp, response.TxHash)
	if _, err = rp.WaitForTransaction(response.TxHash); err != nil {
		return err
	}

	// Log & return
	fmt.Printf("Successfully claimed %.6f RPL in rewards.", math.RoundDown(eth.WeiToEth(canClaim.RplAmount), 6))
	return nil

}

// Download the rewards files for the provided indices
func downloadRewardsFiles(cfg *config.RocketPoolConfig, intervals []uint64, cids []string) bool {
	network := fmt.Sprint(cfg.Smartnode.Network.Value.(config.Network))

	for i := 0; i < len(intervals); i++ {
		index := intervals[i]
		cid := cids[i]
		path := cfg.Smartnode.GetRewardsTreePath(index, false)

		// Try to download from the primary URL
		primary := fmt.Sprintf(primaryFileGateway, cid, network, index)
		fmt.Printf("Downloading %s... ", primary)
		resp, err := http.Get(primary)
		if err != nil {
			// Try to download from the secondary URL
			fmt.Printf("error: %s\n", err.Error())
			secondary := fmt.Sprintf(secondaryFileGateway, cid, network, index)
			fmt.Printf("Trying fallback (%s)... ", secondary)
			resp, err = http.Get(primary)
			if err != nil {
				// Error out
				fmt.Printf("error: %s\n", err.Error())
				return false
			}
		}
		fmt.Print("done!")

		// Save the file
		defer resp.Body.Close()
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			fmt.Printf("Error reading interval %d file: %s", index, err.Error())
			return false
		}
		err = ioutil.WriteFile(path, body, 0644)
		if err != nil {
			fmt.Printf("Error saving interval %d file to %s: %s", index, path, err.Error())
			return false
		}
	}

	return true
}
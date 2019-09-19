package tests

import (
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/suite"

	"github.com/reserve-protocol/rsv-beta/abi"
	"github.com/reserve-protocol/rsv-beta/soltools"
)

func TestReserve(t *testing.T) {
	suite.Run(t, new(ReserveSuite))
}

type ReserveSuite struct {
	TestSuite
}

var (
	// Compile-time check that ReserveSuite implements the interfaces we think it does.
	// If it does not implement these interfaces, then the corresponding setup and teardown
	// functions will not actually run.
	_ suite.BeforeTest       = &ReserveSuite{}
	_ suite.SetupAllSuite    = &ReserveSuite{}
	_ suite.TearDownAllSuite = &ReserveSuite{}
)

var coverageEnabled = os.Getenv("COVERAGE_ENABLED") != ""

// SetupSuite runs once, before all of the tests in the suite.
func (s *ReserveSuite) SetupSuite() {
	s.setup()
	if coverageEnabled {
		s.createSlowCoverageNode()
	} else {
		s.createFastNode()
	}
}

// TearDownSuite runs once, after all of the tests in the suite.
func (s *ReserveSuite) TearDownSuite() {
	if coverageEnabled {
		// Write coverage profile to disk.
		s.Assert().NoError(s.node.(*soltools.Backend).WriteCoverage())

		// Close the node.js process.
		s.Assert().NoError(s.node.(*soltools.Backend).Close())

		// Process coverage profile into an HTML report.
		if out, err := exec.Command("npx", "istanbul", "report", "html").CombinedOutput(); err != nil {
			fmt.Println()
			fmt.Println("I generated coverage information in coverage/coverage.json.")
			fmt.Println("I tried to process it with `istanbul` to turn it into a readable report, but failed.")
			fmt.Println("The error I got when running istanbul was:", err)
			fmt.Println("Istanbul's output was:\n" + string(out))
		}
	}
}

// BeforeTest runs before each test in the suite.
func (s *ReserveSuite) BeforeTest(suiteName, testName string) {
	// Re-deploy Reserve and store a handle to the Go binding and the contract address.
	reserveAddress, tx, reserve, err := abi.DeployReserve(s.signer, s.node)
	s.requireTx(tx, err)()
	s.reserve = reserve
	s.reserveAddress = reserveAddress

	// Get the Go binding and contract address for the new ReserveEternalStorage contract.
	s.eternalStorageAddress, err = s.reserve.GetEternalStorageAddress(nil)
	s.Require().NoError(err)
	s.eternalStorage, err = abi.NewReserveEternalStorage(s.eternalStorageAddress, s.node)
	s.Require().NoError(err)

	deployerAddress := s.account[0].address()

	s.logParsers = map[common.Address]logParser{
		s.reserveAddress:        s.reserve,
		s.eternalStorageAddress: s.eternalStorage,
	}

	// Make the deployment account a minter, pauser, and freezer.
	s.requireTx(s.reserve.ChangeMinter(s.signer, deployerAddress))(
		abi.ReserveMinterChanged{NewMinter: deployerAddress},
	)
	s.requireTx(s.reserve.ChangePauser(s.signer, deployerAddress))(
		abi.ReservePauserChanged{NewPauser: deployerAddress},
	)
	s.requireTx(s.reserve.ChangeFreezer(s.signer, deployerAddress))(
		abi.ReserveFreezerChanged{NewFreezer: deployerAddress},
	)
}

func (s *ReserveSuite) TestDeploy() {}

func (s *ReserveSuite) TestBalanceOf() {
	s.assertBalance(common.Address{}, bigInt(0))
}

func (s *ReserveSuite) TestName() {
	name, err := s.reserve.Name(nil)
	s.NoError(err)
	s.Equal("Reserve", name)
}

func (s *ReserveSuite) TestSymbol() {
	symbol, err := s.reserve.Symbol(nil)
	s.NoError(err)
	s.Equal("RSV", symbol)
}

func (s *ReserveSuite) TestDecimals() {
	decimals, err := s.reserve.Decimals(nil)
	s.NoError(err)
	s.Equal(uint8(18), decimals)
}

func (s *ReserveSuite) TestChangeName() {
	const newName, newSymbol = "Flamingo", "MGO"
	s.requireTx(
		s.reserve.ChangeName(s.signer, newName, newSymbol),
	)(
		abi.ReserveNameChanged{
			NewName:   newName,
			NewSymbol: newSymbol,
		},
	)

	// Check new name.
	name, err := s.reserve.Name(nil)
	s.NoError(err)
	s.Equal(newName, name)

	// Check new symbol.
	symbol, err := s.reserve.Symbol(nil)
	s.NoError(err)
	s.Equal(newSymbol, symbol)
}

func (s *ReserveSuite) TestChangeNameFailsForNonOwner() {
	const newName, newSymbol = "Flamingo", "MGO"
	s.requireTxFails(
		s.reserve.ChangeName(signer(s.account[2]), newName, newSymbol),
	)
}

func (s *ReserveSuite) TestAllowsMinting() {
	recipient := common.BigToAddress(bigInt(1))
	amount := bigInt(100)

	// Mint to recipient.
	s.requireTx(s.reserve.Mint(s.signer, recipient, amount))(
		mintingTransfer(recipient, amount),
	)

	// Check that balances are as expected.
	s.assertBalance(s.account[0].address(), bigInt(0))
	s.assertBalance(recipient, amount)
	s.assertTotalSupply(amount)
}

func (s *ReserveSuite) TestTransfer() {
	sender := s.account[1]
	recipient := common.BigToAddress(bigInt(1))
	amount := bigInt(100)

	// Mint to sender.
	s.requireTx(s.reserve.Mint(s.signer, sender.address(), amount))(
		mintingTransfer(sender.address(), amount),
	)

	// Transfer from sender to recipient.
	s.requireTx(s.reserve.Transfer(signer(sender), recipient, amount))(
		abi.ReserveTransfer{
			From:  sender.address(),
			To:    recipient,
			Value: amount,
		},
	)

	// Check that balances are as expected.
	s.assertBalance(sender.address(), bigInt(0))
	s.assertBalance(recipient, amount)
	s.assertBalance(s.account[0].address(), bigInt(0))
	s.assertTotalSupply(amount)
}

func (s *ReserveSuite) TestTransferExceedsFunds() {
	sender := s.account[1]
	recipient := common.BigToAddress(bigInt(1))
	amount := bigInt(100)
	smallAmount := bigInt(10) // must be smaller than amount

	// Mint smallAmount to sender.
	s.requireTx(s.reserve.Mint(s.signer, sender.address(), smallAmount))(
		mintingTransfer(sender.address(), smallAmount),
	)

	// Transfer from sender to recipient should fail.
	s.requireTxFails(s.reserve.Transfer(signer(sender), recipient, amount))

	// Balances should be as we expect.
	s.assertBalance(sender.address(), smallAmount)
	s.assertBalance(recipient, bigInt(0))
	s.assertBalance(s.account[0].address(), bigInt(0))
	s.assertTotalSupply(smallAmount)
}

// As long as Minting cannot overflow a uint256, then `transferFrom` cannot overflow.
func (s *ReserveSuite) TestMintWouldOverflow() {
	interestingRecipients := []common.Address{
		common.BigToAddress(bigInt(1)),
		common.BigToAddress(bigInt(255)),
		common.BigToAddress(bigInt(256)),
		common.BigToAddress(bigInt(256)),
		common.BigToAddress(maxUint160()),
		common.BigToAddress(minInt160AsUint160()),
	}
	for _, recipient := range interestingRecipients {
		smallAmount := bigInt(10) // must be smaller than amount
		overflowCausingAmount := maxUint256()
		overflowCausingAmount = overflowCausingAmount.Sub(overflowCausingAmount, bigInt(8))

		// Mint smallAmount to recipient.
		s.requireTx(s.reserve.Mint(s.signer, recipient, smallAmount))(
			mintingTransfer(recipient, smallAmount),
		)

		// Mint a quantity large enough to cause overflow in totalSupply i.e.
		// `10 + (uint256::MAX - 8) > uint256::MAX`
		s.requireTxFails(s.reserve.Mint(s.signer, recipient, overflowCausingAmount))
	}
}

func (s *ReserveSuite) TestApprove() {
	owner := s.account[1]
	spender := s.account[2]
	amount := bigInt(53)

	// Owner approves spender.
	s.requireTx(s.reserve.Approve(signer(owner), spender.address(), amount))(
		abi.ReserveApproval{Holder: owner.address(), Spender: spender.address(), Value: amount},
	)

	// Approval should be reflected in allowance.
	s.assertAllowance(owner.address(), spender.address(), amount)

	// Shouldn't be symmetric.
	s.assertAllowance(spender.address(), owner.address(), bigInt(0))

	// Balances shouldn't change.
	s.assertBalance(owner.address(), bigInt(0))
	s.assertBalance(spender.address(), bigInt(0))
	s.assertTotalSupply(bigInt(0))
}

func (s *ReserveSuite) TestIncreaseAllowance() {
	owner := s.account[1]
	spender := s.account[2]
	amount := bigInt(2000)

	// Owner approves spender through increaseAllowance.
	s.requireTx(s.reserve.IncreaseAllowance(signer(owner), spender.address(), amount))(
		abi.ReserveApproval{Holder: owner.address(), Spender: spender.address(), Value: amount},
	)

	// Approval should be reflected in allowance.
	s.assertAllowance(owner.address(), spender.address(), amount)

	// Shouldn't be symmetric.
	s.assertAllowance(spender.address(), owner.address(), bigInt(0))

	// Balances shouldn't change.
	s.assertBalance(owner.address(), bigInt(0))
	s.assertBalance(spender.address(), bigInt(0))
	s.assertTotalSupply(bigInt(0))
}

func (s *ReserveSuite) TestIncreaseAllowanceWouldOverflow() {
	owner := s.account[1]
	spender := s.account[2]
	initialAmount := bigInt(10)

	// Owner approves spender for initial amount.
	s.requireTx(s.reserve.IncreaseAllowance(signer(owner), spender.address(), initialAmount))(
		abi.ReserveApproval{Holder: owner.address(), Spender: spender.address(), Value: initialAmount},
	)

	// Owner should not be able to increase approval high enough to overflow a uint256.
	s.requireTxFails(s.reserve.IncreaseAllowance(signer(owner), spender.address(), maxUint256()))
}

func (s *ReserveSuite) TestDecreaseAllowance() {
	owner := s.account[1]
	spender := s.account[2]
	initialAmount := bigInt(10)
	decrease := bigInt(6)
	final := bigInt(4)

	// Owner approves spender for initial amount.
	s.requireTx(s.reserve.IncreaseAllowance(signer(owner), spender.address(), initialAmount))(
		abi.ReserveApproval{Holder: owner.address(), Spender: spender.address(), Value: initialAmount},
	)

	// Owner decreases allowance.
	s.requireTx(s.reserve.DecreaseAllowance(signer(owner), spender.address(), decrease))(
		abi.ReserveApproval{Holder: owner.address(), Spender: spender.address(), Value: final},
	)

	// Allowance should be as we expect.
	s.assertAllowance(owner.address(), spender.address(), final)

	// Balances shouldn't change.
	s.assertBalance(owner.address(), bigInt(0))
	s.assertBalance(spender.address(), bigInt(0))
	s.assertTotalSupply(bigInt(0))
}

func (s *ReserveSuite) TestDecreaseAllowanceUnderflow() {
	owner := s.account[1]
	spender := s.account[2]
	initialAmount := bigInt(10)
	decrease := bigInt(11)

	// Owner approves spender for initial amount.
	s.requireTx(s.reserve.IncreaseAllowance(signer(owner), spender.address(), initialAmount))(
		abi.ReserveApproval{Holder: owner.address(), Spender: spender.address(), Value: initialAmount},
	)

	// Owner decreases allowance fails because of underflow.
	s.requireTxFails(s.reserve.DecreaseAllowance(signer(owner), spender.address(), decrease))

	// Allowance should be as we expect.
	s.assertAllowance(owner.address(), spender.address(), initialAmount)

	// Balances shouldn't change.
	s.assertBalance(owner.address(), bigInt(0))
	s.assertBalance(spender.address(), bigInt(0))
	s.assertTotalSupply(bigInt(0))

	// Allowances shouldn't change
	s.assertAllowance(owner.address(), spender.address(), initialAmount)
}

func (s *ReserveSuite) TestDecreaseAllowanceSpenderFrozen() {
	deployerAddress := s.account[0].address()
	spender := s.account[1]
	owner := s.account[2]

	// Owner approves spender for initial amount.
	s.requireTx(s.reserve.IncreaseAllowance(signer(owner), spender.address(), bigInt(10)))(
		abi.ReserveApproval{Holder: owner.address(), Spender: spender.address(), Value: bigInt(10)},
	)

	// Freeze spender.
	s.requireTx(s.reserve.Freeze(s.signer, spender.address()))(
		abi.ReserveFrozen{Freezer: deployerAddress, Account: spender.address()},
	)

	// The owner CAN decrease the allowance of a frozen spender.
	s.requireTx(s.reserve.DecreaseAllowance(signer(owner), spender.address(), bigInt(2)))(
		abi.ReserveApproval{Holder: owner.address(), Spender: spender.address(), Value: bigInt(8)},
	)
}

func (s *ReserveSuite) TestPausing() {
	banker := s.account[1]
	amount := bigInt(1000)
	approveAmount := bigInt(1)
	recipient := s.account[2]
	spender := s.account[3]

	// Give banker funds. Minting is allowed while unpaused.
	s.requireTx(s.reserve.Mint(s.signer, banker.address(), amount))(
		mintingTransfer(banker.address(), amount),
	)
	s.assertBalance(banker.address(), amount)

	// Approve spender to spend bankers funds.
	s.requireTx(s.reserve.Approve(signer(banker), spender.address(), approveAmount))(
		abi.ReserveApproval{Holder: banker.address(), Spender: spender.address(), Value: approveAmount},
	)
	s.assertAllowance(banker.address(), spender.address(), approveAmount)

	// Pause.
	s.requireTx(s.reserve.Pause(s.signer))(
		abi.ReservePaused{Account: s.account[0].address()},
	)

	// Minting is not allowed while paused.
	s.requireTxFails(s.reserve.Mint(s.signer, recipient.address(), amount))

	// Transfers from are not allowed while paused.
	s.requireTxFails(s.reserve.TransferFrom(s.signer, banker.address(), recipient.address(), amount))
	s.assertBalance(recipient.address(), bigInt(0))
	s.assertBalance(banker.address(), amount)

	// Transfers are not allowed while paused.
	s.requireTxFails(s.reserve.Transfer(signer(banker), recipient.address(), amount))
	s.assertBalance(recipient.address(), bigInt(0))
	s.assertBalance(banker.address(), amount)

	// Approving is not allowed while paused.
	s.requireTxFails(s.reserve.Approve(signer(banker), spender.address(), amount))
	s.assertAllowance(banker.address(), spender.address(), approveAmount)

	// IncreaseAllowance is not allowed while paused.
	s.requireTxFails(s.reserve.IncreaseAllowance(signer(banker), spender.address(), amount))
	s.assertAllowance(banker.address(), spender.address(), approveAmount)

	// DecreaseAllowance is not allowed while paused.
	s.requireTxFails(s.reserve.DecreaseAllowance(signer(banker), spender.address(), amount))
	s.assertAllowance(banker.address(), spender.address(), approveAmount)

	// Unpause.
	s.requireTx(s.reserve.Unpause(s.signer))(
		abi.ReserveUnpaused{Account: s.account[0].address()},
	)

	// Transfers are allowed while unpaused.
	s.requireTx(s.reserve.Transfer(signer(banker), recipient.address(), amount))(
		abi.ReserveTransfer{From: banker.address(), To: recipient.address(), Value: amount},
	)
	s.assertBalance(recipient.address(), amount)

	// Approving is allowed while unpaused.
	s.requireTx(s.reserve.Approve(signer(banker), spender.address(), bigInt(2)))(
		abi.ReserveApproval{Holder: banker.address(), Spender: spender.address(), Value: bigInt(2)},
	)
	s.assertAllowance(banker.address(), spender.address(), bigInt(2))

	// DecreaseAllowance is allowed while unpaused.
	s.requireTx(s.reserve.DecreaseAllowance(signer(banker), spender.address(), approveAmount))(
		abi.ReserveApproval{Holder: banker.address(), Spender: spender.address(), Value: bigInt(1)},
	)
	s.assertAllowance(banker.address(), spender.address(), approveAmount)

	// IncreaseAllowance is allowed while unpaused.
	s.requireTx(s.reserve.IncreaseAllowance(signer(banker), spender.address(), approveAmount))(
		abi.ReserveApproval{Holder: banker.address(), Spender: spender.address(), Value: bigInt(2)},
	)
	s.assertAllowance(banker.address(), spender.address(), bigInt(2))
}

func (s *ReserveSuite) TestFreezeTransferOut() {
	target := s.account[1]
	recipient := s.account[2]

	// Give target funds.
	amount := bigInt(1)
	s.requireTx(s.reserve.Mint(s.signer, target.address(), amount))(
		mintingTransfer(target.address(), amount),
	)

	// Freeze target.
	s.requireTx(s.reserve.Freeze(s.signer, target.address()))(
		abi.ReserveFrozen{Freezer: s.account[0].address(), Account: target.address()},
	)

	// Frozen account shouldn't be able to transfer.
	s.requireTxFails(s.reserve.Transfer(signer(target), recipient.address(), amount))

	// Unfreeze target.
	s.requireTx(s.reserve.Unfreeze(s.signer, target.address()))(
		abi.ReserveUnfrozen{Freezer: s.account[0].address(), Account: target.address()},
	)

	// Unfrozen account should be able to transfer again.
	s.requireTx(s.reserve.Transfer(signer(target), recipient.address(), amount))(
		abi.ReserveTransfer{From: target.address(), To: recipient.address(), Value: amount},
	)
	s.assertBalance(recipient.address(), amount)
}

func (s *ReserveSuite) TestFreezeTransferIn() {
	target := s.account[1]
	amount := bigInt(200)

	// Mint initial funds to deployer.
	s.requireTx(s.reserve.Mint(s.signer, s.account[0].address(), amount))(
		mintingTransfer(s.account[0].address(), amount),
	)

	// Freeze target.
	s.requireTx(s.reserve.Freeze(s.signer, target.address()))(
		abi.ReserveFrozen{Freezer: s.account[0].address(), Account: target.address()},
	)

	// Frozen account shouldn't be able to receive funds.
	s.requireTxFails(s.reserve.Transfer(s.signer, target.address(), amount))

	// Unfreeze target.
	s.requireTx(s.reserve.Unfreeze(s.signer, target.address()))(
		abi.ReserveUnfrozen{Freezer: s.account[0].address(), Account: target.address()},
	)

	// Frozen account should be able to receive funds again.
	s.requireTx(s.reserve.Transfer(s.signer, target.address(), amount))(
		abi.ReserveTransfer{From: s.account[0].address(), To: target.address(), Value: amount},
	)
	s.assertBalance(target.address(), amount)
}

func (s *ReserveSuite) TestFreezeApprovals() {
	target := s.account[1]
	recipient := s.account[2]

	// Freeze target.
	s.requireTx(s.reserve.Freeze(s.signer, target.address()))(
		abi.ReserveFrozen{Freezer: s.account[0].address(), Account: target.address()},
	)

	// Frozen account shouldn't be able to create approvals.
	s.requireTxFails(s.reserve.Approve(signer(target), recipient.address(), bigInt(1)))
	s.requireTxFails(s.reserve.IncreaseAllowance(signer(target), recipient.address(), bigInt(1)))
	s.assertAllowance(target.address(), recipient.address(), bigInt(0))

	// Unfreeze target.
	s.requireTx(s.reserve.Unfreeze(s.signer, target.address()))(
		abi.ReserveUnfrozen{Freezer: s.account[0].address(), Account: target.address()},
	)

	// Unfrozen account should be able to create approvals again.
	s.requireTx(s.reserve.Approve(signer(target), recipient.address(), bigInt(1)))(
		abi.ReserveApproval{Holder: target.address(), Spender: recipient.address(), Value: bigInt(1)},
	)
	s.requireTx(s.reserve.IncreaseAllowance(signer(target), recipient.address(), bigInt(1)))(
		abi.ReserveApproval{Holder: target.address(), Spender: recipient.address(), Value: bigInt(2)},
	)
	s.assertAllowance(target.address(), recipient.address(), bigInt(2))

	// Freeze recipient.
	s.requireTx(s.reserve.Freeze(s.signer, recipient.address()))(
		abi.ReserveFrozen{Freezer: s.account[0].address(), Account: recipient.address()},
	)

	// Frozen recipient should not be able to receive approvals.
	s.requireTxFails(s.reserve.Approve(signer(target), recipient.address(), bigInt(1)))
	s.requireTxFails(s.reserve.IncreaseAllowance(signer(target), recipient.address(), bigInt(1)))
	s.assertAllowance(target.address(), recipient.address(), bigInt(2))

	// Unfreeze recipient.
	s.requireTx(s.reserve.Unfreeze(s.signer, recipient.address()))(
		abi.ReserveUnfrozen{Freezer: s.account[0].address(), Account: recipient.address()},
	)

	// Unfrozen account should be able to receive approvals again.
	s.requireTx(s.reserve.Approve(signer(target), recipient.address(), bigInt(11)))(
		abi.ReserveApproval{Holder: target.address(), Spender: recipient.address(), Value: bigInt(11)},
	)
	s.requireTx(s.reserve.IncreaseAllowance(signer(target), recipient.address(), bigInt(7)))(
		abi.ReserveApproval{Holder: target.address(), Spender: recipient.address(), Value: bigInt(18)},
	)
	s.assertAllowance(target.address(), recipient.address(), bigInt(18))
}

func (s *ReserveSuite) TestFreezeTransferFrom() {
	deployerAddress := s.account[0].address()
	target := s.account[1]
	recipient := s.account[2]
	middleman := s.account[3]

	// Approve target and middleman to transfer funds.
	initialAmount := bigInt(12)
	s.requireTx(s.reserve.Mint(s.signer, s.account[0].address(), initialAmount))(
		mintingTransfer(deployerAddress, initialAmount),
	)
	s.requireTx(s.reserve.Approve(s.signer, target.address(), initialAmount))(
		abi.ReserveApproval{Holder: deployerAddress, Spender: target.address(), Value: initialAmount},
	)
	s.requireTx(s.reserve.Approve(s.signer, middleman.address(), initialAmount))(
		abi.ReserveApproval{Holder: deployerAddress, Spender: middleman.address(), Value: initialAmount},
	)
	s.assertAllowance(s.account[0].address(), target.address(), initialAmount)
	s.assertAllowance(s.account[0].address(), middleman.address(), initialAmount)

	////////////////////////////////////
	// Freeze target.
	s.requireTx(s.reserve.Freeze(s.signer, target.address()))(
		abi.ReserveFrozen{Freezer: deployerAddress, Account: target.address()},
	)

	// Frozen account shouldn't be able to call transferFrom.
	s.requireTxFails(s.reserve.TransferFrom(signer(target), deployerAddress, recipient.address(), initialAmount))
	s.assertBalance(recipient.address(), bigInt(0))

	// Unfreeze target.
	s.requireTx(s.reserve.Unfreeze(s.signer, target.address()))(
		abi.ReserveUnfrozen{Freezer: deployerAddress, Account: target.address()},
	)

	// Unfrozen account should now be able to call transferFrom.
	s.requireTx(s.reserve.TransferFrom(signer(target), deployerAddress, recipient.address(), bigInt(2)))(
		abi.ReserveTransfer{From: deployerAddress, To: recipient.address(), Value: bigInt(2)},
		abi.ReserveApproval{Holder: deployerAddress, Spender: target.address(), Value: bigInt(12 - 2)},
	)
	s.assertBalance(recipient.address(), bigInt(2))

	////////////////////////////////////
	// Freeze middleman
	s.requireTx(s.reserve.Freeze(s.signer, middleman.address()))(
		abi.ReserveFrozen{Freezer: deployerAddress, Account: middleman.address()},
	)

	// Frozen account shouldn't be able to call transferFrom.
	s.requireTxFails(s.reserve.TransferFrom(signer(middleman), s.account[0].address(), recipient.address(), bigInt(5)))
	s.assertBalance(recipient.address(), bigInt(2))

	// Unfreeze middleman.
	s.requireTx(s.reserve.Unfreeze(s.signer, middleman.address()))(
		abi.ReserveUnfrozen{Freezer: deployerAddress, Account: middleman.address()},
	)

	// Unfrozen account should now be able to call transferFrom.
	s.requireTx(s.reserve.TransferFrom(signer(middleman), s.account[0].address(), recipient.address(), bigInt(5)))(
		abi.ReserveTransfer{From: deployerAddress, To: recipient.address(), Value: bigInt(5)},
		abi.ReserveApproval{Holder: deployerAddress, Spender: middleman.address(), Value: bigInt(12 - 5)},
	)
	s.assertBalance(recipient.address(), bigInt(7))

	////////////////////////////////////
	// Freeze recipient.
	s.requireTx(s.reserve.Freeze(s.signer, recipient.address()))(
		abi.ReserveFrozen{Freezer: deployerAddress, Account: recipient.address()},
	)

	// Shouldn't be able to call transferFrom to a frozen account.
	s.requireTxFails(s.reserve.TransferFrom(signer(target), s.account[0].address(), recipient.address(), initialAmount))
	s.assertBalance(recipient.address(), bigInt(7))

	// Unfreeze recipient.
	s.requireTx(s.reserve.Unfreeze(s.signer, recipient.address()))(
		abi.ReserveUnfrozen{Freezer: deployerAddress, Account: recipient.address()},
	)

	// Unfrozen account should now be able to call transferFrom.
	s.requireTx(s.reserve.TransferFrom(signer(target), deployerAddress, recipient.address(), bigInt(3)))(
		abi.ReserveTransfer{From: deployerAddress, To: recipient.address(), Value: bigInt(3)},
		abi.ReserveApproval{Holder: deployerAddress, Spender: target.address(), Value: bigInt(10 - 3)},
	)
	s.assertBalance(recipient.address(), bigInt(10))
}

func (s *ReserveSuite) TestFreezeApprove() {
	deployerAddress := s.account[0].address()
	target := s.account[1]

	// Freeze target.
	s.requireTx(s.reserve.Freeze(s.signer, target.address()))(
		abi.ReserveFrozen{Freezer: deployerAddress, Account: target.address()},
	)

	// Should not be able to approve frozen target.
	s.requireTxFails(s.reserve.Approve(s.signer, target.address(), bigInt(1)))

	// Unfreeze target.
	s.requireTx(s.reserve.Unfreeze(s.signer, target.address()))(
		abi.ReserveUnfrozen{Freezer: deployerAddress, Account: target.address()},
	)

	// Should be able to approve unfrozen target.
	s.requireTx(s.reserve.Approve(s.signer, target.address(), bigInt(1)))(
		abi.ReserveApproval{Holder: deployerAddress, Spender: target.address(), Value: bigInt(1)},
	)
}

func (s *ReserveSuite) TestFreezeIncreaseAllowance() {
	deployerAddress := s.account[0].address()
	target := s.account[1]
	owner := s.account[2]

	// Freeze target.
	s.requireTx(s.reserve.Freeze(s.signer, target.address()))(
		abi.ReserveFrozen{Freezer: deployerAddress, Account: target.address()},
	)

	// Should not be able to increase allowance frozen target.
	s.requireTxFails(s.reserve.IncreaseAllowance(signer(owner), target.address(), bigInt(1)))
	s.assertAllowance(owner.address(), target.address(), bigInt(0))

	// Unfreeze target.
	s.requireTx(s.reserve.Unfreeze(s.signer, target.address()))(
		abi.ReserveUnfrozen{Freezer: deployerAddress, Account: target.address()},
	)

	// Should be able to increase allowance unfrozen target.
	s.requireTx(s.reserve.IncreaseAllowance(signer(owner), target.address(), bigInt(1)))(
		abi.ReserveApproval{Holder: owner.address(), Spender: target.address(), Value: bigInt(1)},
	)
	s.assertAllowance(owner.address(), target.address(), bigInt(1))

}

func (s *ReserveSuite) TestFreezeDecreaseAllowance() {
	deployerAddress := s.account[0].address()
	spender := s.account[1]
	owner := s.account[2]

	// Increase allowance to set up for decrease.
	s.requireTx(s.reserve.IncreaseAllowance(signer(owner), spender.address(), bigInt(6)))(
		abi.ReserveApproval{Holder: owner.address(), Spender: spender.address(), Value: bigInt(6)},
	)

	// Freeze spender.
	s.requireTx(s.reserve.Freeze(s.signer, spender.address()))(
		abi.ReserveFrozen{Freezer: deployerAddress, Account: spender.address()},
	)

	// Should be able to decrease allowance frozen spender.
	s.requireTx(s.reserve.DecreaseAllowance(signer(owner), spender.address(), bigInt(4)))(
		abi.ReserveApproval{Holder: owner.address(), Spender: spender.address(), Value: bigInt(2)},
	)
	s.assertAllowance(owner.address(), spender.address(), bigInt(2))

	// Unfreeze spender.
	s.requireTx(s.reserve.Unfreeze(s.signer, spender.address()))(
		abi.ReserveUnfrozen{Freezer: deployerAddress, Account: spender.address()},
	)

	// Should still be able to decrease allowance unfrozen spender.
	s.requireTx(s.reserve.DecreaseAllowance(signer(owner), spender.address(), bigInt(1)))(
		abi.ReserveApproval{Holder: owner.address(), Spender: spender.address(), Value: bigInt(1)},
	)
	s.assertAllowance(owner.address(), spender.address(), bigInt(1))
}

func (s *ReserveSuite) TestWiping() {
	deployerAddress := s.account[0].address()
	target := s.account[1]

	// Give target funds.
	amount := bigInt(100)
	s.requireTx(s.reserve.Mint(s.signer, target.address(), amount))(
		mintingTransfer(target.address(), amount),
	)

	// Should not be able to wipe zero address
	s.requireTx(s.reserve.Freeze(s.signer, zeroAddress()))(
		abi.ReserveFrozen{Freezer: deployerAddress, Account: zeroAddress()},
	)
	s.requireTxFails(s.reserve.Wipe(s.signer, target.address()))

	// Should not be able to wipe target before freezing them.
	s.requireTxFails(s.reserve.Wipe(s.signer, target.address()))

	// Freeze target.
	s.requireTx(s.reserve.Freeze(s.signer, target.address()))(
		abi.ReserveFrozen{Freezer: deployerAddress, Account: target.address()},
	)

	// Target should still have funds.
	s.assertBalance(target.address(), amount)

	// Should not be able to immediately wipe target.
	s.requireTxFails(s.reserve.Wipe(s.signer, target.address()))

	if simulatedBackend, ok := s.node.(backend); ok {
		// Simulate advancing time.
		simulatedBackend.AdjustTime(24 * time.Hour * 40)

		// Should be able to wipe target now.
		s.requireTx(s.reserve.Wipe(s.signer, target.address()))(
			abi.ReserveTransfer{From: target.address(), To: zeroAddress(), Value: amount},
			abi.ReserveWiped{Freezer: deployerAddress, Wiped: target.address()},
		)

		// Target should have zero funds.
		s.assertBalance(target.address(), bigInt(0))
	} else {
		fmt.Fprintln(os.Stderr, "\nCan't simulate advancing time in coverage mode -- not testing wiping after a delay.")
		fmt.Fprintln(os.Stderr)
	}
}

func (s *ReserveSuite) TestMintingBurningChain() {
	deployerAddress := s.account[0].address()
	// Mint to recipient.
	recipient := s.account[1]
	amount := bigInt(100)

	s.requireTx(s.reserve.Mint(s.signer, recipient.address(), amount))(
		mintingTransfer(recipient.address(), amount),
	)

	s.assertBalance(recipient.address(), amount)
	s.assertTotalSupply(amount)

	// Approve signer for burning.
	s.requireTx(s.reserve.Approve(signer(recipient), deployerAddress, amount))(
		abi.ReserveApproval{Holder: recipient.address(), Spender: deployerAddress, Value: amount},
	)

	// Burn from recipient.
	s.requireTx(s.reserve.BurnFrom(s.signer, recipient.address(), amount))(
		abi.ReserveTransfer{From: recipient.address(), To: zeroAddress(), Value: amount},
		abi.ReserveApproval{Holder: recipient.address(), Spender: deployerAddress, Value: bigInt(0)},
	)

	s.assertBalance(recipient.address(), bigInt(0))
	s.assertTotalSupply(bigInt(0))
}

func (s *ReserveSuite) TestMintingTransferBurningChain() {
	deployerAddress := s.account[0].address()
	recipient := s.account[1]
	amount := bigInt(100)

	// Mint to recipient.
	s.requireTx(s.reserve.Mint(s.signer, recipient.address(), amount))(
		mintingTransfer(recipient.address(), amount),
	)

	s.assertBalance(recipient.address(), amount)
	s.assertTotalSupply(amount)

	// Transfer to target.
	target := s.account[2]
	s.requireTx(s.reserve.Transfer(signer(recipient), target.address(), amount))(
		abi.ReserveTransfer{From: recipient.address(), To: target.address(), Value: amount},
	)

	s.assertBalance(target.address(), amount)
	s.assertBalance(recipient.address(), bigInt(0))

	// Approve signer for burning.
	s.requireTx(s.reserve.Approve(signer(target), s.account[0].address(), amount))(
		abi.ReserveApproval{Holder: target.address(), Spender: s.account[0].address(), Value: amount},
	)

	// Burn from target.
	s.requireTx(s.reserve.BurnFrom(s.signer, target.address(), amount))(
		abi.ReserveTransfer{From: target.address(), To: zeroAddress(), Value: amount},
		abi.ReserveApproval{Holder: target.address(), Spender: deployerAddress, Value: bigInt(0)},
	)

	s.assertBalance(target.address(), bigInt(0))
	s.assertBalance(recipient.address(), bigInt(0))
	s.assertTotalSupply(bigInt(0))
}

func (s *ReserveSuite) TestBurnFromWouldUnderflow() {
	deployerAddress := s.account[0].address()
	// Mint to recipient.
	recipient := s.account[1]
	amount := bigInt(100)
	causesUnderflowAmount := bigInt(101)

	s.assertTotalSupply(bigInt(0))
	s.requireTx(s.reserve.Mint(s.signer, recipient.address(), amount))(
		mintingTransfer(recipient.address(), amount),
	)

	s.assertBalance(recipient.address(), amount)
	s.assertTotalSupply(amount)

	// Approve signer for burning.
	s.requireTx(s.reserve.Approve(signer(recipient), deployerAddress, amount))(
		abi.ReserveApproval{Holder: recipient.address(), Spender: deployerAddress, Value: amount},
	)

	// Burn from recipient.
	s.requireTxFails(s.reserve.BurnFrom(s.signer, recipient.address(), causesUnderflowAmount))

	s.assertBalance(recipient.address(), amount)
	s.assertTotalSupply(amount)
}

func (s *ReserveSuite) TestTransferFrom() {
	sender := s.account[1]
	middleman := s.account[2]
	recipient := s.account[3]

	amount := bigInt(1)
	s.requireTx(s.reserve.Mint(s.signer, sender.address(), amount))(
		mintingTransfer(sender.address(), amount),
	)
	s.assertBalance(sender.address(), amount)
	s.assertBalance(middleman.address(), bigInt(0))
	s.assertBalance(recipient.address(), bigInt(0))
	s.assertTotalSupply(amount)

	// Approve middleman to transfer funds from the sender.
	s.requireTx(s.reserve.Approve(signer(sender), middleman.address(), amount))(
		abi.ReserveApproval{Holder: sender.address(), Spender: middleman.address(), Value: amount},
	)
	s.assertAllowance(sender.address(), middleman.address(), amount)

	// transferFrom allows the msg.sender to send an existing approval to an arbitrary destination.
	s.requireTx(s.reserve.TransferFrom(signer(middleman), sender.address(), recipient.address(), amount))(
		abi.ReserveTransfer{From: sender.address(), To: recipient.address(), Value: amount},
		abi.ReserveApproval{Holder: sender.address(), Spender: middleman.address(), Value: bigInt(0)},
	)
	s.assertBalance(sender.address(), bigInt(0))
	s.assertBalance(middleman.address(), bigInt(0))
	s.assertBalance(recipient.address(), amount)

	// Allowance should have been decreased by the transfer
	s.assertAllowance(sender.address(), middleman.address(), bigInt(0))
	// transfers should not change totalSupply.
	s.assertTotalSupply(amount)
}

func (s *ReserveSuite) TestTransferFromWouldUnderflow() {
	sender := s.account[1]
	middleman := s.account[2]
	recipient := s.account[3]

	approveAmount := bigInt(2)
	s.requireTx(s.reserve.Mint(s.signer, sender.address(), approveAmount))(
		mintingTransfer(sender.address(), approveAmount),
	)
	s.assertBalance(sender.address(), approveAmount)
	s.assertBalance(middleman.address(), bigInt(0))
	s.assertBalance(recipient.address(), bigInt(0))
	s.assertTotalSupply(approveAmount)

	// Approve middleman to transfer funds from the sender.
	s.requireTx(s.reserve.Approve(signer(sender), middleman.address(), approveAmount))(
		abi.ReserveApproval{Holder: sender.address(), Spender: middleman.address(), Value: approveAmount},
	)
	s.assertAllowance(sender.address(), middleman.address(), approveAmount)

	// now reduce the approveAmount in the sender's account to less than the approval for the middleman
	s.requireTx(s.reserve.Transfer(signer(sender), recipient.address(), bigInt(1)))(
		abi.ReserveTransfer{From: sender.address(), To: recipient.address(), Value: bigInt(1)},
	)

	// Attempt to transfer more funds than the sender's current balance, but
	// passing the approval checks. Should fail when subtracting value from
	// sender's current balance.
	s.requireTxFails(s.reserve.TransferFrom(signer(middleman), sender.address(), recipient.address(), approveAmount))

	s.assertBalance(sender.address(), bigInt(1))
	s.assertBalance(middleman.address(), bigInt(0))
	s.assertBalance(recipient.address(), bigInt(1))

	// Allowance should not have been changed
	s.assertAllowance(sender.address(), middleman.address(), approveAmount)
	// should not change totalSupply.
	s.assertTotalSupply(approveAmount)
}

///////////////////////

func (s *ReserveSuite) TestPauseFailsForNonPauser() {
	s.requireTxFails(s.reserve.Pause(signer(s.account[2])))
}

func (s *ReserveSuite) TestUnpauseFailsForNonPauser() {
	deployerAddress := s.account[0].address()
	s.requireTx(s.reserve.Pause(s.signer))(
		abi.ReservePaused{Account: deployerAddress},
	)
	s.requireTxFails(s.reserve.Unpause(signer(s.account[1])))
}

func (s *ReserveSuite) TestChangePauserFailsForNonPauser() {
	s.requireTxFails(s.reserve.ChangePauser(signer(s.account[2]), s.account[1].address()))
}

///////////////////////

func (s *ReserveSuite) TestFreezeFailsForNonFreezer() {
	criminal := common.BigToAddress(bigInt(1))
	s.requireTxFails(s.reserve.Freeze(signer(s.account[2]), criminal))
}

func (s *ReserveSuite) TestUnfreezeFailsForNonFreezer() {
	deployerAddress := s.account[0].address()
	criminal := common.BigToAddress(bigInt(1))
	s.requireTx(s.reserve.Freeze(s.signer, criminal))(
		abi.ReserveFrozen{Freezer: deployerAddress, Account: criminal},
	)
	s.requireTxFails(s.reserve.Unfreeze(signer(s.account[1]), criminal))
}

func (s *ReserveSuite) TestChangeFreezerFailsForNonFreezer() {
	s.requireTxFails(s.reserve.ChangeFreezer(signer(s.account[2]), s.account[1].address()))
}

func (s *ReserveSuite) TestWipeFailsForNonFreezer() {
	criminal := common.BigToAddress(bigInt(1))
	s.requireTxFails(s.reserve.Wipe(signer(s.account[2]), criminal))
}

///////////////////////

func (s *ReserveSuite) TestMintFailsForNonMinter() {
	recipient := common.BigToAddress(bigInt(1))
	s.requireTxFails(s.reserve.Mint(signer(s.account[2]), recipient, bigInt(7)))
}

func (s *ReserveSuite) TestChangeMinterFailsForNonMinter() {
	s.requireTxFails(s.reserve.ChangeMinter(signer(s.account[2]), s.account[1].address()))
}

///////////////////////

func (s *ReserveSuite) TestUpgrade() {
	recipient := s.account[1]
	amount := big.NewInt(100)

	// Mint to recipient.
	s.requireTx(s.reserve.Mint(s.signer, recipient.address(), amount))(
		mintingTransfer(recipient.address(), amount),
	)

	// Deploy new contract.
	newKey := s.account[2]
	newTokenAddress, tx, newToken, err := abi.DeployReserveV2(signer(newKey), s.node)
	s.requireTx(tx, err)()

	// Make the switch.
	s.requireTx(s.reserve.NominateNewOwner(s.signer, newTokenAddress))()
	s.requireTx(newToken.CompleteHandoff(signer(newKey), s.reserveAddress)) /*
		not asserting events because there are a lot and we don't care much about them
	*/

	// Old token should not be functional.
	s.requireTxFails(s.reserve.Mint(s.signer, recipient.address(), big.NewInt(1500)))
	s.requireTxFails(s.reserve.Transfer(signer(recipient), s.account[3].address(), big.NewInt(10)))
	s.requireTxFails(s.reserve.Pause(s.signer))
	s.requireTxFails(s.reserve.Unpause(s.signer))

	// assertion function for new token
	assertBalance := func(address common.Address, amount *big.Int) {
		balance, err := newToken.BalanceOf(nil, address)
		s.NoError(err)
		s.Equal(amount.String(), balance.String()) // assert.Equal can mis-compare big.Ints, so compare strings instead
	}

	// New token should be functional.
	assertBalance(recipient.address(), amount)
	s.logParsers[newTokenAddress] = newToken
	s.requireTx(newToken.ChangeMinter(signer(newKey), newKey.address()))(
		abi.ReserveV2MinterChanged{NewMinter: newKey.address()},
	)
	s.requireTx(newToken.ChangePauser(signer(newKey), newKey.address()))(
		abi.ReserveV2PauserChanged{NewPauser: newKey.address()},
	)
	s.requireTx(newToken.Mint(signer(newKey), recipient.address(), big.NewInt(1500)))(
		abi.ReserveV2Transfer{From: zeroAddress(), To: recipient.address(), Value: bigInt(1500)},
	)
	s.requireTx(newToken.Transfer(signer(recipient), s.account[3].address(), big.NewInt(10)))(
		abi.ReserveV2Transfer{From: recipient.address(), To: s.account[3].address(), Value: bigInt(10)},
	)
	s.requireTx(newToken.Pause(signer(newKey)))(
		abi.ReserveV2Paused{Account: newKey.address()},
	)
	s.requireTx(newToken.Unpause(signer(newKey)))(
		abi.ReserveV2Unpaused{Account: newKey.address()},
	)
	assertBalance(recipient.address(), big.NewInt(100+1500-10))
	assertBalance(s.account[3].address(), big.NewInt(10))
}

// Test that we can use the escape hatch in ReserveEternalStorage.
func (s *ReserveSuite) TestEternalStorageEscapeHatch() {
	assertOwner := func(expected common.Address) {
		owner, err := s.eternalStorage.Owner(nil)
		s.NoError(err)
		s.Equal(expected, owner)
	}

	assertEscapeHatch := func(expected common.Address) {
		escapeHatch, err := s.eternalStorage.EscapeHatch(nil)
		s.NoError(err)
		s.Equal(expected, escapeHatch)
	}

	// Check that owner and escapeHatch are initialized in the way we expect.
	assertOwner(s.reserveAddress)
	assertEscapeHatch(s.account[0].address())

	newEscapeHatch := s.account[3]

	// Change escapeHatch address and check it is what we expect.
	s.requireTx(s.eternalStorage.TransferEscapeHatch(s.signer, newEscapeHatch.address()))(
		abi.ReserveEternalStorageEscapeHatchTransferred{
			OldEscapeHatch: s.account[0].address(),
			NewEscapeHatch: newEscapeHatch.address(),
		},
	)

	// Check that escapeHatch changed and owner didn't.
	assertOwner(s.reserveAddress)
	assertEscapeHatch(newEscapeHatch.address())

	newOwner := s.account[4]

	// Change owner as escapeHatch account.
	s.requireTx(s.eternalStorage.TransferOwnership(signer(newEscapeHatch), newOwner.address()))(
		abi.ReserveEternalStorageOwnershipTransferred{
			OldOwner: s.reserveAddress,
			NewOwner: newOwner.address(),
		},
	)

	// Check that owner changed and escapeHatch didn't.
	assertOwner(newOwner.address())
	assertEscapeHatch(newEscapeHatch.address())

	// Check that owner cannot change escapeHatch.
	s.requireTxFails(s.eternalStorage.TransferEscapeHatch(signer(newOwner), s.account[5].address()))

	// Check that escapeHatch can make the change the owner could not.
	s.requireTx(s.eternalStorage.TransferEscapeHatch(signer(newEscapeHatch), s.account[5].address()))(
		abi.ReserveEternalStorageEscapeHatchTransferred{
			OldEscapeHatch: newEscapeHatch.address(),
			NewEscapeHatch: s.account[5].address(),
		},
	)
}

// Test that setBalance works as expected on ReserveEternalStorage.
// It is not used by the current Reserve contract, but is present as a bit
// of potential future-proofing for upgrades.
func (s *ReserveSuite) TestEternalStorageSetBalance() {
	newOwner := s.account[1]
	amount := bigInt(1300)

	// Check that we can't call setBalance before becoming the owner.
	s.requireTxFails(s.eternalStorage.SetBalance(signer(newOwner), newOwner.address(), amount))

	// Transfer ownership of Eternal Storage to external account.
	s.requireTx(s.eternalStorage.TransferOwnership(s.signer, newOwner.address()))(
		abi.ReserveEternalStorageOwnershipTransferred{
			OldOwner: s.reserveAddress,
			NewOwner: newOwner.address(),
		},
	)

	// Check that we can now call setBalance.
	s.requireTx(s.eternalStorage.SetBalance(signer(newOwner), newOwner.address(), amount))( /* assert zero events */ )

	// Balance should have changed.
	balance, err := s.eternalStorage.Balance(nil, newOwner.address())
	s.NoError(err)
	s.Equal(amount.String(), balance.String())
}

//////////////// Utility

func maxUint256() *big.Int {
	z := bigInt(1)
	z = z.Lsh(z, 256)
	z = z.Sub(z, bigInt(1))
	return z
}

func maxUint160() *big.Int {
	z := bigInt(1)
	z = z.Lsh(z, 160)
	z = z.Sub(z, bigInt(1))
	return z
}

func minInt160AsUint160() *big.Int {
	z := bigInt(1)
	z = z.Lsh(z, 159)
	return z
}

func bigInt(n uint32) *big.Int {
	return big.NewInt(int64(n))
}

func zeroAddress() common.Address {
	return common.BigToAddress(bigInt(0))
}

func mintingTransfer(to common.Address, value *big.Int) abi.ReserveTransfer {
	return abi.ReserveTransfer{
		From:  common.BigToAddress(bigInt(0)),
		To:    to,
		Value: value,
	}
}

func burningTransfer(from common.Address, value *big.Int) abi.ReserveTransfer {
	return abi.ReserveTransfer{
		From:  from,
		To:    common.BigToAddress(bigInt(0)),
		Value: value,
	}
}
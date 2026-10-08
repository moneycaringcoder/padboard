package main

// Contract addresses (lowercase) and event topics for the tracked launchpads.
// Every platform launches tokens into Uniswap v4 pools on Ethereum mainnet, so
// volume and price share one PoolManager Swap path; launches and fees are
// decoded per platform.

const (
	poolManager = "0x000000000004444c5dc75cb358380d2e3de08a90"
	ethAddr     = "0x0000000000000000000000000000000000000000"
	wethAddr    = "0xc02aaa39b223fe8d0a0e5c4f27ead9083c756cc2"

	selDecimals    = "0x313ce567"
	selSymbol      = "0x95d89b41"
	selName        = "0x06fdde03"
	selTotalSupply = "0x18160ddd"
	selMetaSender  = "0x03ee438c" // metadataURI()
	selMetaStk     = "0x77a4d559" // metadataUri()
	// balanceOf(0x…dEaD)
	callBalanceDead = "0x70a08231000000000000000000000000000000000000000000000000000000000000dead"
)

type platform struct {
	Key, Name, Site string
	Idx             int    // 1-based position in platforms; picks the --cN accent colour
	Token           string // the platform's own token, launched on itself ("" if none)
	TokenSymbol     string
	Logo            string // static asset path
	Genesis         uint64 // first block with a launch; the platform's sync cursor starts here
	Factories       []string
	Hooks           []string
	Escrows         []string
	Lockers         []string
	// Address credited with the platform share of trading fees.
	PlatformRecipient string
	// Launch fee in ETH at deployment; updated from events.
	InitialLaunchFee float64
	// metadata-pointer selector on launched tokens; "" when launch events carry it.
	MetaSelector  string
	ImageAPI      string // optional fmt URL (token address) returning JSON with an image, when pointers fail
	HasGraduation bool
	// Human explanations rendered in the methodology notes.
	FeeNote, GraduationNote string
}

var stockereum = &platform{
	Key:          "stockereum",
	Name:         "Stockereum",
	Site:         "https://stockereum.com",
	Token:        "0x75e2fc69ff2ac12af65ba7d321bbf4f878c535d2",
	TokenSymbol:  "STOCKER",
	Logo:         "/static/stockereum.svg",
	Genesis:      25899301,
	MetaSelector: selMetaStk,
	Factories: []string{
		"0x4c402aa92f166be2d3d9ba1b2879bd2b5616a26d", // LaunchFactory (first)
		"0xc6b080ded03c3382476a76345e79f82bd480977b", // LaunchFactory (current)
	},
	Hooks: []string{
		"0xafed2c6e0d906520ca17143a8918ce6d54b128cc",
		"0x322dcec4958c14e021a9f1cd49df11b9457968cc",
	},
	Escrows: []string{
		"0x0b943b855912214f0a73ca80b73deaccea1ab8da",
		"0xacefe251da006887da41c063d06cc82a060824ba",
	},
	PlatformRecipient: "0x4d0fca0bb85ce7b91acac6a55b3ee48ee2eb587b", // buyback & burn contract
	InitialLaunchFee:  0.001,
	FeeNote: "Hook fee (1–3%, set per launch) on both buys and sells, credited to FeeEscrow on every swap: creator or holder-distributor share vs platform share (platform share goes to the buyback & burn contract). " +
		"Buy-side volume is net of the hook fee because it is taken before the pool swap. Launch fee 0.001 ETH to treasury.",
	GraduationNote: "No bonding curve: every token opens as a full Uniswap v4 pool at launch. 'Graduated' on stockereum.com is an off-chain market-cap label and is not tracked here.",
}

var sender = &platform{
	Key:           "sender",
	Name:          "Sender",
	Site:          "https://sender.family",
	Token:         "0x47accd13264d8f954105256faac8376ce6a55999",
	TokenSymbol:   "SEND",
	Logo:          "/static/sender.png",
	Genesis:       25899301,
	MetaSelector:  selMetaSender,
	HasGraduation: true,
	Factories: []string{
		"0x2126625df80b8bd01294139b26241b121cab9fff", // SendItFactory
		"0x3b0004c50c4a8584c99599a9063fa2b9a140b707", // SendItFactory
		"0xd08b94a1372be4b5fad7698b5b2adda01119f2aa", // SendItFactory
		"0x8d37c2981bdf809567092fd458b6bf3e97ee860c", // SendItFactory (current v1)
		"0x90c7fa39a0121a50be9086d46dac36b7323b6cb8", // SendItFactory v2 (proxy)
	},
	Hooks: []string{
		"0xee83560bb83fa38dcd3c1060233ddc7933c520cc", // LaunchGuardHook (v2, from block 26110133)
		"0xa6cc4ffa9ebaefd8ab7d3d018b0ae31ec5b760cc", // LaunchGuardHook (v2 R2, from block 26134578)
	},
	Lockers: []string{
		"0x6fe376175878cbb06a44aa9ba08f29cbe85a422f",
		"0xed801d51ba98cb935351e40cc58e44670f2c769f",
		"0xe85895079c86d13685270c101dc6ac62097d8afc",
		"0x844833d98765344ee206598a81f31ad3cc15b1a4",
		"0x36dc86399094090d07b9041fbf1d54f965cf9319", // v2
		"0x898464980f0374375dba1327f9d136d725f3c449", // v2
	},
	InitialLaunchFee: 0.002,
	FeeNote: "v1: 1% Uniswap LP fee accrues inside the locked position and is split 70/30 creator/protocol when FeesCollected fires; creator payouts are CreatorClaimed. " +
		"v2: the hook charges the fee per swap and distributes it immediately (FeeDistributed: creator / treasury / holders). Launch fee 0.002 ETH, 50% burn / 50% protocol.",
	GraduationNote: "Graduated when the locked pool's quote reserve crosses the locker threshold (Graduated event on the LiquidityLocker).",
}

var stroid = &platform{
	Key:     "stroid",
	Name:    "Stroid",
	Site:    "https://stroid.fun",
	Logo:    "/static/stroid.png",
	Genesis: 24942014,
	Factories: []string{
		"0x86b00cc0e3da64af96dc90c52bb4f755fc243b6a", // Launchpad V1
		"0xbbdf89fd1700bcbd906b743610f88a1fbebc5b21", // Launchpad V2
		"0x75d9ef48e30bcb0b658c1d387233fb52ebf9a4fe", // Launchpad V3
	},
	Hooks: []string{
		"0xa2dcd7bf7ff3c014a855bf00799ccf07e6c800cc", // fee hook V1
		"0x8a22e2a5768c72751f2da3e3b904365203b100cc", // fee hook V2
		"0x0d62529346ac2c61f5c0582210d01214687bc0cc", // fee hook V3
	},
	PlatformRecipient: "0xf33764058d47ee96436b506c2739f9be70ee4e5e", // protocolWallet() on all three hooks
	ImageAPI:          "https://api.stroid.fun/tokens/%s?chain=1",   // icons only; most on-chain metadata hosts are gone
	FeeNote: "Hook fee in ETH on both buys and sells (tiered by market cap, 1.5% down to 0.4% on V3), accrued per swap (FeeAccrued): creator share (25% by default) vs protocol. " +
		"V3 partner and module cuts come out of the protocol share and are shown as partners. No LP fee; the launch liquidity is burned. Buy-side volume is net of the hook fee.",
	GraduationNote: "No bonding curve: every token opens as a native-ETH Uniswap v4 pool with burned liquidity, so nothing graduates.",
}

var clanker = &platform{
	Key:       "clanker",
	Name:      "Clanker",
	Site:      "https://clanker.world",
	Logo:      "/static/clanker.png",
	Genesis:   23622577,
	Factories: []string{"0x6c8599779b03b00aaae63c6378830919abb75473"}, // Clanker v4 factory
	Hooks:     []string{"0x6c24d0bcc264ef6a740754a11ca579b9d225e8cc"}, // static-fee hook
	Lockers:   []string{"0x00c4b21889145cf0d99f2e05919103e0c3991974"}, // LP locker with fee conversion
	Escrows:   []string{"0xa9c0a423f0092176fc48d7b50a1fcae8cf5bb441"}, // fee locker: recipients withdraw here
	FeeNote: "LP fee (set per pool, typically 1%; early swaps pay a decaying sniper fee) accrues inside the locked position and is counted only when collected (ClaimedRewards): the first reward recipient as creator, any others as partners. " +
		"Clanker's 20% protocol cut is counted when the hook claims it (ClaimProtocolFees). Fees still sitting in positions are not shown, so totals run behind accrual.",
	GraduationNote: "No bonding curve: launches open as full WETH Uniswap v4 pools, so nothing graduates.",
}

var platforms = []*platform{stockereum, sender, stroid, clanker}

func init() {
	for i, p := range platforms {
		p.Idx = i + 1
	}
}

func platformByKey(k string) *platform {
	for _, p := range platforms {
		if p.Key == k {
			return p
		}
	}
	return nil
}

// Event topics (keccak256 of the canonical signature).
const (
	tStkLaunchedV1    = "0x2a673da5ccdc5ec20113d73429b29a536f6969391cfcb7b388bba350c50974bd" // Launched(address,address,address,bytes32,uint160,int24,uint256,string)
	tStkLaunchedV2    = "0xcc4bd3d58f3b2210224800e3960de24e656e19d40c902db18b82f2330c11a677" // Launched(address,address,address,bytes32,uint160,int24,uint256,string,uint24,address)
	tStkLaunchOpened  = "0x98bc720bb4bac22f4cb3d04f544d2e55e9c5b1929ba661943cd40b6d6955ff5f" // LaunchOpened(bytes32,address,address,address,uint160,int24,int24,uint128,uint24,bool)
	tStkCredited      = "0x4e45da441832cf53bdaa69235704fc0575e68210f459ee1562911024b12967d5" // Credited(address,address,uint256)
	tStkClaimed       = "0x913c992353dc81b7a8ba31496c484e9b6306bd2f6c509a649a38fdf5e1c953b2" // Claimed(address,address,address,uint256)
	tStkCreationFee   = "0x65cf44d7c3dc10549f322afbe745b2a569e6e2a177f9465749a393afbc9c354f" // CreationFeeSet(uint256)
	tSndLaunched      = "0x1c04c61b4c31484e624c938b3880ab830ebf83cbd67c0f468dc163c764b52960" // Launched(address,address,bytes32,uint256,string,string,string,uint256)
	tSndLaunchedV2    = "0xe484ac3200e22536098c94373a3935474f552bb0690efaf2dc6b978fd90ff416" // LaunchedV2(bytes32,address,address,address,address,uint256,int24,...)
	tSndFeesCollected = "0x60335fc6da5dde7dc66197a635e02eb0dafc452a299f77200b64358c94cdb616" // FeesCollected(uint256,uint256,uint256,uint256,uint256)
	tSndCreatorClaim  = "0xcbc2fc69aabe0441aa335ee08f9703a8c8f0ee78ac68f7f8ea002f5fc32378c2" // CreatorClaimed(uint256,address,uint256,uint256)
	tSndGraduated     = "0x5f5c1612369b9980b4dda7f56ad1aefadfedd3afb6f766181e1460409b7c13b9" // Graduated(uint256,address,uint256)
	tSndFeeDistrib    = "0xd8e6083148f56423fbb88b063161b9c1fe21884ec7aac3dc7374af1495f92a09" // FeeDistributed(bytes32,uint256,uint256,address,uint256,uint256,uint256)
	tSndFeesClaimed   = "0xfd4ee9dab4282188f41ec255b98f52c0a86486347400e939905a76f8eaf2fd49" // FeesClaimed(bytes32,uint256,uint256,address,uint256,uint256,uint256)
	tSndLaunchFee     = "0xc799be5eb19a1a6d6ba7368d21e2bc367c8a335e4a07cd3d954482e6f714d3c5" // LaunchFeeUpdated(uint256)
	tSndLaunchFeeV2   = "0x0fd958ac60db1437ae35514054402c4e597e81881e4b6dd47a6509bd89121428" // LaunchFeeUpdated(uint256,uint256)
	tStrLaunchedV1    = "0xd2fd17cf2bd629785b9965e8258e2e48b0547fb356a9fac81d30b2bb4eacbbc7" // TokenLaunched(address,address,string,string,string,uint256,uint256)
	tStrLaunchedV2    = "0x5c4ff1559e694ef966456d7a0b2530071089dea7a0c5c1cac7a734c9f6d4cb55" // TokenLaunched(address,address,address,string,string,string,uint256,uint256)
	tStrLaunchedV3    = "0x37447771ff513c9ab4ea987c6464ef6a4be02293e90462eb14f3d834d985643a" // TokenLaunched(address,address,address,address,string,string,string,uint256,uint256)
	tStrFeeAccrued    = "0x8bfe3c7ea5ffc0d8951d20f096f55944070816210948f2f56db10b4e7cf54bee" // FeeAccrued(address,uint256,uint256,uint256)
	tStrPartnerFee    = "0x0197a90e2726f79560fe18edc678d162014653cb4535ea3e468da8f35be72935" // PartnerFeeAccrued(address,address,uint256)
	tStrModuleFee     = "0xde543d3bdfbeef5284b338f143889df468be2522b1c5ac45f4ddc5a8e9e04564" // ModuleFeeAccrued(address,address,uint256)
	tStrClaimed       = "0xd8138f8a3f377c5259ca548e70e4c2de94f129f5a11036a15b69513cba2b426a" // Claimed(address,uint256)
	tClkCreated       = "0x9299d1d1a88d8e1abdc591ae7a167a6bc63a8f17d695804e9091ee33aa89fb67" // TokenCreated(address,address,address,string,string,string,string,string,int24,address,bytes32,address,address,address,uint256,address[])
	tClkRewards       = "0x21d15f71483b597e8f0009e83b90b2117f6f98c185d7173857dddcae5eb8546a" // ClaimedRewards(address,uint256,uint256,uint256[],uint256[])
	tClkProtocolFees  = "0x175b790d44599ca70432cc8d1406504cb3a28fc13ff995c06dde6663412b211a" // ClaimProtocolFees(address,uint256)
	tClkClaimTokens   = "0xf98eaa9c1f790e5c18b1f227bd5bade62600f9f3e3587c7644b90c50b9bf13c5" // ClaimTokens(address,address,uint256)
	tPMInitialize     = "0xdd466e674ea557f56295e2d0218a125ea4b4f0f6f3307b95f85e6110838d6438" // Initialize(bytes32,address,address,uint24,int24,address,uint160,int24)
	tPMSwap           = "0x40e9cecb9f5f1f1c5b9c97dec2917b7ee92e57ba5563708daca94dd84ad7112f" // Swap(bytes32,address,int128,int128,uint160,uint128,int24,uint24)
)

// platformByAddr maps every tracked contract to its platform.
var platformByAddr = func() map[string]*platform {
	m := map[string]*platform{}
	for _, p := range platforms {
		for _, group := range [][]string{p.Factories, p.Hooks, p.Escrows, p.Lockers} {
			for _, a := range group {
				m[a] = p
			}
		}
	}
	return m
}()

// contractsOf lists every tracked contract of the given platforms.
func contractsOf(group []*platform) []string {
	var out []string
	for _, p := range group {
		for _, g := range [][]string{p.Factories, p.Hooks, p.Escrows, p.Lockers} {
			out = append(out, g...)
		}
	}
	return out
}

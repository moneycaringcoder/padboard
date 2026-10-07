package main

// Contract addresses (lowercase) and event topics for the tracked launchpads.
// Both platforms launch tokens into Uniswap v4 pools on Ethereum mainnet.

const (
	poolManager = "0x000000000004444c5dc75cb358380d2e3de08a90"
	ethAddr     = "0x0000000000000000000000000000000000000000"
	wethAddr    = "0xc02aaa39b223fe8d0a0e5c4f27ead9083c756cc2"

	// Earliest factory deployment across both platforms (Stockereum v2 factory).
	genesisBlock = 25899301

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
	Token           string // the platform's own token, launched on itself
	TokenSymbol     string
	Logo            string // static asset path
	Factories       []string
	Hooks           []string
	Escrows         []string
	Lockers         []string
	// Address credited with the platform share of trading fees.
	PlatformRecipient string
	// Launch fee in ETH at deployment; updated from events.
	InitialLaunchFee float64
	// Human explanations rendered in the methodology notes.
	FeeNote, GraduationNote string
}

var stockereum = &platform{
	Key:         "stockereum",
	Name:        "Stockereum",
	Site:        "https://stockereum.com",
	Token:       "0x75e2fc69ff2ac12af65ba7d321bbf4f878c535d2",
	TokenSymbol: "STOCKER",
	Logo:        "/static/stockereum.svg",
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
	Key:         "sender",
	Name:        "Sender",
	Site:        "https://sender.family",
	Token:       "0x47accd13264d8f954105256faac8376ce6a55999",
	TokenSymbol: "SEND",
	Logo:        "/static/sender.png",
	Factories: []string{
		"0x2126625df80b8bd01294139b26241b121cab9fff", // SendItFactory
		"0x3b0004c50c4a8584c99599a9063fa2b9a140b707", // SendItFactory
		"0xd08b94a1372be4b5fad7698b5b2adda01119f2aa", // SendItFactory
		"0x8d37c2981bdf809567092fd458b6bf3e97ee860c", // SendItFactory (current v1)
		"0x90c7fa39a0121a50be9086d46dac36b7323b6cb8", // SendItFactory v2 (proxy)
	},
	Hooks: []string{
		"0xa6cc4ffa9ebaefd8ab7d3d018b0ae31ec5b760cc", // LaunchGuardHook (v2)
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

var platforms = []*platform{stockereum, sender}

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

func allPlatformAddrs() []string {
	out := make([]string, 0, len(platformByAddr))
	for a := range platformByAddr {
		out = append(out, a)
	}
	return out
}

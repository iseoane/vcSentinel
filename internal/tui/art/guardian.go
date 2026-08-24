package art

// guardianGrid is the splash artwork: a 44x40 pixel bust of the original
// Sentinel guardian — crimson helmet, blue armor, open escorzo palm with the
// luminous core — over a purple comic panel with a black band on the right.
// 'E' marks the eyes and 'C'/'c'/'y' the palm core: the only state-reactive
// keys. '.' is the purple panel, '!' the black band.
var guardianGrid = []string{
	"..................................!!!!!!!!!!",
	"...........#MMMM#.................!!!!!!!!!!",
	".........#MMMMMMMM#...............!!!!!!!!!!",
	"........#MMMMMMMMMM#..............!!!!!!!!!!",
	"........#MMMMMMMMMMm#.............!!!!!!!!!!",
	".......#MMMMMMMMMMMm#.............!!!!!!!!!!",
	".......#MM#GGGGGG#Mm#.............!!!!!!!!!!",
	".......#MM#EE##EE#Mm#.............!!!!!!!!!!",
	".......#MM#EE##EE#Mm#.............!!!!!!!!!!",
	".......#MM#######Mm#..............!!!!!!!!!!",
	".......#MM#GGGGG#Mm#..............!!!!!!!!!!",
	".......#MM#Gg#gG#Mm#..............!!!!!!!!!!",
	"........#MMMMMMMMm#...............!!!!!!!!!!",
	".........#MMMMMMm#................!!!!!!!!!!",
	"...........#gggg#.................!!!!!!!!!!",
	"..........##gggg##................!!!!!!!!!!",
	"........##BBB##...................!!!!!!!!!!",
	".....#LBBBB####BBB##..............!!!!!!!!!!",
	"...#LLBBBBB###RRRR#...............!!!!!!!!!!",
	"..#LBBBBBBB##RRRRRR#b#............!!!!!!!!!!",
	".#LBBBBBBBB##RRRRRR#bb#........####!!!!!!!!!",
	"#BBBBBBBBBB##RRRRRR#bb#........LBLB#!!!!!!!!",
	"#BBBBBBBBBB##RRR#bBB#bb#..###..LBLB#!!!!!!!!",
	".#BBBBBBBB##bb#BBBB#bb#...LBL..LBLB#LR#!!!!!",
	".#BBBBBBB##bb#..#BBBBBBB#.LBL..LBLB#LR#LR#!!",
	"..#BBBB##bb#....#BBBBBB##.LBL..LBLB#LR#LR#!!",
	"..#BBB##bb#...............################!!",
	"..#BBB##bb#..............#LBBBBBBBBBBBBBBB!!",
	"..#BBB##bb#..............#BBBBcccccccBBB#!!!",
	"..#BBB##bb#..............#BccccCCCCCCcBBB#!!",
	"..#BBB##bb#..............#BccCCCCCCCcBBB#!!!",
	"..#BBB##bb#..............#BccCCCCCCCcBBB#!!!",
	"..#BBB##bb#..............#BccCCCCCCCcBBB#!!!",
	"..#BBB##bb#..............#BccCCCcccccBBB#!!!",
	"..#BBB##bb#..............#BBcccccccccBBB#!!!",
	"..#BBB##bb#..............#BBBBBBBBBBBBBB#!!!",
	"..#BBB##bb#..............##BBBBBBBBBBBB##!!!",
	"..#BBB##bb#..............##BBBBB###BBBB##!!!",
	"..#BBB##bb#.................##BB##!##B#!!!!!",
	"#BBBBBBBBBbb#.....................!!!!!!!!!!",
}

// compactGrid is the dashboard mascot: 18x16 pixels (8 text rows) carrying
// the same identity — helmet, eyes, and a palm-core peeking from the corner.
var compactGrid = []string{
	"....#MMMMM#.......",
	"..#MMMMMMMMM#.....",
	".#MM#GGGGG#Mm#....",
	".#MM#EE#EE#Mm#.###",
	".#MM#######Mm##cc#",
	".#MM#GGGG#Mm#BCcc#",
	"..#MMMMMMm##BB#c#.",
	"...#MMMMm##BB##...",
}

// guardianPalette maps every grid key to its color. Reactive keys (E, C, c,
// y) hold placeholder entries here; WithState overwrites them.
var guardianPalette = Palette{
	'.': PurplePanel,
	'!': PanelBlack,
	'#': Outline,
	'B': ArmorBlue,
	'b': ArmorShade,
	'L': ArmorLight,
	'M': HelmetMagma,
	'm': HelmetShade,
	'R': PadRed,
	'G': FacePlate,
	'g': FaceShade,
	'Y': CoreBright,
	'y': CoreGlow,
	'W': Highlight,
	'E': CoreBright,
	'C': CoreBright,
	'c': CoreGlow,
}

// Splash returns the full guardian bust frame for the given state.
func Splash(s State) Frame {
	return WithState(Frame{Grid: guardianGrid, Pal: guardianPalette}, s)
}

// Compact returns the small mascot frame for the given state.
func Compact(s State) Frame {
	return WithState(Frame{Grid: compactGrid, Pal: guardianPalette}, s)
}

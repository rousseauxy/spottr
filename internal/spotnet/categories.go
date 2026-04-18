package spotnet

// Categories maps the Spotnet category/subcategory system.
// Main categories are 0-3; subcategories use bitmasks per slot (a/b/c/d).
// Ported from SpotCategories.php in the original Spotweb source.

const (
	CatImage = 0
	CatAudio = 1
	CatGame  = 2
	CatApp   = 3
)

type Category struct {
	ID   int
	Name string
	Subs []SubCategory
}

type SubCategory struct {
	Slot  string // "a", "b", "c", "d"
	Bit   int
	Name  string
}

var MainCategories = []Category{
	{ID: CatImage, Name: "Image"},
	{ID: CatAudio, Name: "Audio"},
	{ID: CatGame,  Name: "Game"},
	{ID: CatApp,   Name: "Application"},
}

// SubCategoryNames provides human-readable names for display/filtering.
// Key: "category.slot.bit"
var SubCategoryNames = map[string]string{
	// Image subcategories (cat 0)
	"0.a.1": "SD", "0.a.2": "HD", "0.a.4": "UHD",
	"0.b.1": "Film", "0.b.2": "Series", "0.b.4": "Clip", "0.b.8": "Documentary", "0.b.16": "Sports",
	"0.c.1": "Dutch", "0.c.2": "English", "0.c.4": "German", "0.c.8": "French",
	"0.d.1": "Animated", "0.d.2": "Adult",

	// Audio subcategories (cat 1)
	"1.a.1": "MP3", "1.a.2": "Lossless", "1.a.4": "Audiobook", "1.a.8": "Radio",
	"1.b.1": "Pop", "1.b.2": "Rock", "1.b.4": "Electronic", "1.b.8": "Classical",
	"1.b.16": "HipHop", "1.b.32": "Jazz", "1.b.64": "World",
	"1.c.1": "Dutch", "1.c.2": "English", "1.c.4": "German", "1.c.8": "French",

	// Game subcategories (cat 2)
	"2.a.1": "PC", "2.a.2": "PlayStation", "2.a.4": "Xbox", "2.a.8": "Nintendo",
	"2.a.16": "Mobile", "2.a.32": "Other",

	// App subcategories (cat 3)
	"3.a.1": "Windows", "3.a.2": "MacOS", "3.a.4": "Linux", "3.a.8": "Android", "3.a.16": "iOS",
	"3.b.1": "Utility", "3.b.2": "Office", "3.b.4": "Graphics", "3.b.8": "Security",
}

// CategoryName returns the main category name for a category ID.
func CategoryName(cat int) string {
	switch cat {
	case CatImage:
		return "Image"
	case CatAudio:
		return "Audio"
	case CatGame:
		return "Game"
	case CatApp:
		return "Application"
	default:
		return "Unknown"
	}
}

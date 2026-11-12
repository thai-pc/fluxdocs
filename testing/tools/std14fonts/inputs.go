package main

// Upstream inputs, pinned by SHA-256. The tool refuses to run on anything
// else, so its output is reproducible from these files alone. Sources (see
// THIRD_PARTY_NOTICES.md for the archive URLs and their checksums):
//
//   - Liberation 2.1.5, liberation-fonts-ttf-2.1.5.tar.gz from the GitHub
//     release of github.com/liberationfonts/liberation-fonts (-liberation:
//     the extracted liberation-fonts-ttf-2.1.5 directory);
//   - DejaVu 2.37, dejavu-fonts-ttf-2.37.tar.bz2 from the GitHub release of
//     github.com/dejavu-fonts/dejavu-fonts (-dejavu: the extracted
//     dejavu-fonts-ttf-2.37 directory);
//   - Adobe Core14_AFMs.zip from download.macromedia.com/pub/developer/
//     opentype/tech-notes/ (-afm: the extracted files);
//   - the Adobe Glyph List, github.com/adobe-type-tools/agl-aglfn at commit
//     4036a9ca80a62f64f9de4f7321a9a045ad0ecfd6 (-agl).
var inputSums = map[string]map[string]string{
	"liberation": {
		"LICENSE":                        "93fed46019c38bbe566b479d22148e2e8a1e85ada614accb0211c37b2c61c19b",
		"LiberationMono-Bold.ttf":        "bd62a0672d0b9b6710b01df434c80ad54fa5f0835207eb7b17b7a761463067bb",
		"LiberationMono-BoldItalic.ttf":  "79451f3c09fe25116098853b7a2ca6e2436220ccc11af022979adbcf195be130",
		"LiberationMono-Italic.ttf":      "605c01c711b44480a7508d349dfbf3264e81fa43d69e61cfa7d10b86e764c4d1",
		"LiberationMono-Regular.ttf":     "f2b83c763e8afd21709333370bed4774337fae82267937e2b5aea7e2fbd922c1",
		"LiberationSans-Bold.ttf":        "788abee4c806d660e8aee46689dd8540cd4bb98da03dcc9d171ce3efd99a9173",
		"LiberationSans-BoldItalic.ttf":  "698da70fc191cc5f33ad4d6d3fe830fe4624b898ea2e3169955928b7c491f1ee",
		"LiberationSans-Italic.ttf":      "e5bae5c4cde31f22142753855f4f8fb86da6ff39955ed3c0a11248b0d16948b0",
		"LiberationSans-Regular.ttf":     "76d04c18ea243f426b7de1f3ad208e927008f961dc5945e5aad352d0dfde8ee8",
		"LiberationSerif-Bold.ttf":       "d754ba427cfe0bca54ae052384baa8f842da5bd6550ad4da024ac441e7a7d5ce",
		"LiberationSerif-BoldItalic.ttf": "f17db8af71e24d2066b587546021d4f0b296be389512b658dec3c09affeb11a7",
		"LiberationSerif-Italic.ttf":     "0e3dea9f8d613e006ccfa62201f33e265d19167bd0907725c3e145368b04fc2e",
		"LiberationSerif-Regular.ttf":    "058ea80864aef09a23f45cbec2bb5400bc3dfbdea01c3f10538a21fcb497fb74",
	},
	"dejavu": {
		"LICENSE":            "7a083b136e64d064794c3419751e5c7dd10d2f64c108fe5ba161eae5e5958a93",
		"ttf/DejaVuSans.ttf": "7da195a74c55bef988d0d48f9508bd5d849425c1770dba5d7bfc6ce9ed848954",
	},
	"afm": {
		"Courier-Bold.afm":          "ad0150d4bedcc8877742bf94251fcec13e348dd599d4603f679d92027d1e6e99",
		"Courier-BoldOblique.afm":   "cb82e69ef5f6d421e8f404fe00bb0d993425aae79725331d0ebf847e94e97e92",
		"Courier-Oblique.afm":       "b27103b2a2ef6030c110626597e2ab47bb8279075a039ab9170facb0aa1f70e1",
		"Courier.afm":               "521e0d7c7521efd4be78a5a9c5398e4c67d0771e396115b0346bc4ef74ada53d",
		"Helvetica-Bold.afm":        "b880d96baf56d0cc059f258f60b4d764ef49b555ab9db294b959c0016dee41f2",
		"Helvetica-BoldOblique.afm": "69984a35ca26973a39f261cf83e0d367ea2e6517c590b0b030d4d4a219d9c269",
		"Helvetica-Oblique.afm":     "b4609b71b660a392ac09df35060271a876c2ce66617dd83bf826f742bb9d9721",
		"Helvetica.afm":             "da33f1870474c8e68bfe3e2353ff107ab6c6eea1f9836ce2aaf1e1a07b17982f",
		"MustRead.html":             "be71e055bb2551a6fc8b60dcf094bf40357858980abde86f76cc9a3c144d5403",
		"Symbol.afm":                "3d2128a820375a10de9bc8bf6cfb15ded482c01ca0f95cc0b3277f37ec8bde66",
		"Times-Bold.afm":            "b4a000ed85cb22c6cdd985aa0fd3f6f78ed5079b7c6860dc4f0234e0d0e3c522",
		"Times-BoldItalic.afm":      "93c4744ba955215de02c4aae0b777133442ada2f7ab5a2af30a040b792b3c55d",
		"Times-Italic.afm":          "ed37fa2e6a67b5b17dfd47f36fc7e90df32891a4408860dbc8d4cbbe9959e242",
		"Times-Roman.afm":           "768e1cabea085d489a63da3e80b96bc5abf0ec98d3073c9b4d6ba76e7bccba64",
		"ZapfDingbats.afm":          "a32565c90afd1b57a7008fc567b78d95cf1c22adff5e086094d666d88b039859",
	},
	"agl": {
		"LICENSE.md":       "58147d341e7a34aa2196862395a34d2fd95716c41d5ed26efb59ab0e12f92089",
		"glyphlist.txt":    "a3b2f61ced9f3644cc0d4ecde5c59df34ca286c689d9484a43a710a81c466789",
		"zapfdingbats.txt": "f6394e3cb8a447e84a1dad75d4baaf2aa7f45dc104faf369f4720e1a774ef2dc",
	},
}

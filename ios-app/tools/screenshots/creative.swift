// App Store "Header and Search Results" creative assets, from the raw
// screenshot captures (see make.sh): a backdrop with faint signal rings, and
// the iPhone sharing to a Mac that's connected through it, filling the frame
// (see Layout below for the crops it's designed around).
//
// usage: creative <header|search> <raw dir> <out.png> [light|dark|mint|green]
//   header  3840 x 1646 (21:9), safe area 1646 x 661 centered
//   search  3840 x 2560 (3:2),  safe area 2168 x 1030 centered
import AppKit

let a = CommandLine.arguments
let styles = ["light", "dark", "mint", "green"]
guard a.count == 4 || a.count == 5, ["header", "search"].contains(a[1]),
      a.count == 4 || styles.contains(a[4]) else {
    FileHandle.standardError.write("usage: creative header|search <raw dir> <out.png> [light|dark|mint|green]\n".data(using: .utf8)!)
    exit(2)
}
let kind = a[1], raw = a[2], out = a[3], style = a.count == 5 ? a[4] : "dark"
let W: CGFloat = 3840, H: CGFloat = kind == "header" ? 1646 : 2560
// Art safe areas from Apple's templates, in top-left coordinates.
let safe = kind == "header"
    ? CGRect(x: 1097, y: 493, width: 1646, height: 661)
    : CGRect(x: 836, y: 765, width: 2168, height: 1030)

func load(_ name: String) -> NSImage {
    guard let img = NSImage(contentsOfFile: "\(raw)/\(name)") else {
        FileHandle.standardError.write("missing \(raw)/\(name)\n".data(using: .utf8)!)
        exit(1)
    }
    // Draw at pixel size (a 2x capture reports half its pixels as points).
    if let px = img.representations.first { img.size = NSSize(width: px.pixelsWide, height: px.pixelsHigh) }
    return img
}
let phoneShot = load("iphone-1-share.png")
let macShot = load("mac-1-connect.png")
// The window's content ends about 85% of the way down: drop the empty bottom.
let macSource = NSRect(x: 0, y: macShot.size.height * 0.095, width: macShot.size.width, height: macShot.size.height * 0.905)

func rgb(_ hex: UInt32, _ alpha: CGFloat = 1) -> NSColor {
    NSColor(srgbRed: CGFloat((hex >> 16) & 0xFF) / 255, green: CGFloat((hex >> 8) & 0xFF) / 255,
            blue: CGFloat(hex & 0xFF) / 255, alpha: alpha)
}
/// Top-left based rect to AppKit's bottom-left coordinates.
func flip(_ r: CGRect) -> NSRect { NSRect(x: r.minX, y: H - r.maxY, width: r.width, height: r.height) }

guard let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: Int(W), pixelsHigh: Int(H),
                                 bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                                 colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0) else { exit(1) }
rep.size = NSSize(width: W, height: H)
NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
NSGraphicsContext.current!.imageInterpolation = .high

// Backdrop, rings, glow and title colors for each style.
struct Look { let backdrop: [NSColor]; let ring: UInt32; let ringAlpha: CGFloat; let glow: NSColor; let title: NSColor }
let look: Look = switch style {
case "dark":  // near-black, with a green glow from the app icon behind the devices
    Look(backdrop: [rgb(0x1C1C1E), rgb(0x0A0A0B)], ring: 0xFFFFFF, ringAlpha: 0.07,
         glow: rgb(0x30D158, 0.30), title: .white)
case "mint":  // the pale green behind the screenshots
    Look(backdrop: [rgb(0xEAF8F0), rgb(0xBFE8D2)], ring: 0x0E2A1F, ringAlpha: 0.07,
         glow: rgb(0xFFFFFF, 0.55), title: rgb(0x0E2A1F))
case "green": // the app icon's green
    Look(backdrop: [rgb(0x3FD866), rgb(0x24A04A), rgb(0x0F6A30)], ring: 0xFFFFFF, ringAlpha: 0.20,
         glow: rgb(0xFFFFFF, 0.28), title: .white)
default:      // light: soft gray, like Apple's own product pages
    Look(backdrop: [rgb(0xFBFBFD), rgb(0xE8E8ED)], ring: 0x1D1D1F, ringAlpha: 0.05,
         glow: rgb(0xFFFFFF, 0.7), title: rgb(0x1D1D1F))
}
let stops: [CGFloat] = look.backdrop.count == 3 ? [0, 0.55, 1] : [0, 1]
NSGradient(colors: look.backdrop, atLocations: stops, colorSpace: .sRGB)!
    .draw(in: NSRect(x: 0, y: 0, width: W, height: H), angle: -90)

// Layout (top-left coordinates). The devices fill the frame, leaving only
// thin edges: on an iPhone the App Store shows these assets small, so
// anything less is too tiny to read. App Store Connect's device preview shows
// about the middle 83% of the header's width at full height, and the search
// asset's full width from its top edge down to about y 1850. The headline sits
// as far from the top as the devices are from the sides, so the margins match.
// The iPhone that's sharing stands in front; the Mac connected through it is
// behind, to the right, running off the edge.
let phoneRect: CGRect, macRect: CGRect, hub: CGPoint
if kind == "header" {
    phoneRect = CGRect(x: 380, y: 110, width: 1150, height: 1150 / 0.4675) // runs off the bottom
    macRect = CGRect(x: 1180, y: 170, width: 2500, height: 2500 * macSource.height / macSource.width)
} else {
    phoneRect = CGRect(x: 235, y: 530, width: 1100, height: 1100 / 0.4675)
    macRect = CGRect(x: 1155, y: 590, width: 2450, height: 2450 * macSource.height / macSource.width)
}
hub = CGPoint(x: phoneRect.midX, y: H - (phoneRect.minY + phoneRect.width * 0.5))
let unit = phoneRect.width // scales shadows

// Signal rings spreading out from the iPhone, across the whole canvas.
let ringStep = phoneRect.width * 0.32
for i in 0..<30 {
    let r = ringStep * (CGFloat(i) + 1.2)
    let ring = NSBezierPath(ovalIn: NSRect(x: hub.x - r, y: hub.y - r, width: 2 * r, height: 2 * r))
    ring.lineWidth = unit * 0.012
    rgb(look.ring, max(look.ringAlpha * 0.15, look.ringAlpha * (1 - CGFloat(i) / 30))).setStroke()
    ring.stroke()
}
// A soft glow behind the devices.
let glowCenter = NSPoint(x: (phoneRect.minX + macRect.maxX) / 2, y: H - macRect.midY)
NSGradient(starting: look.glow, ending: look.glow.withAlphaComponent(0))!
    .draw(fromCenter: glowCenter, radius: 0, toCenter: glowCenter, radius: macRect.width * 0.9, options: [])

func shadowed(_ draw: () -> Void) {
    NSGraphicsContext.saveGraphicsState()
    let s = NSShadow()
    s.shadowColor = NSColor.black.withAlphaComponent(0.35)
    s.shadowBlurRadius = unit * 0.12
    s.shadowOffset = NSSize(width: 0, height: -unit * 0.03)
    s.set()
    draw()
    NSGraphicsContext.restoreGraphicsState()
}
/// A device: a dark rounded body with the screenshot inset by the bezel.
func device(_ shot: NSImage, _ frame: CGRect, bezel: CGFloat, corner: CGFloat) {
    let f = flip(frame)
    shadowed {
        rgb(0x111214).setFill()
        NSBezierPath(roundedRect: f, xRadius: corner + bezel, yRadius: corner + bezel).fill()
    }
    let screen = f.insetBy(dx: bezel, dy: bezel)
    NSGraphicsContext.saveGraphicsState()
    NSBezierPath(roundedRect: screen, xRadius: corner, yRadius: corner).addClip()
    shot.draw(in: screen, from: .zero, operation: .sourceOver, fraction: 1)
    NSGraphicsContext.restoreGraphicsState()
}
/// A Mac window: the capture with macOS window corners and a hairline edge.
func window(_ shot: NSImage, _ frame: CGRect) {
    let f = flip(frame), corner = frame.height * 0.03
    let path = NSBezierPath(roundedRect: f, xRadius: corner, yRadius: corner)
    shadowed { rgb(0xFFFFFF).setFill(); path.fill() }
    NSGraphicsContext.saveGraphicsState()
    path.addClip()
    shot.draw(in: f, from: macSource, operation: .sourceOver, fraction: 1)
    NSGraphicsContext.restoreGraphicsState()
    rgb(0x000000, 0.18).setStroke()
    path.lineWidth = 2
    path.stroke()
}

window(macShot, macRect)
device(phoneShot, phoneRect, bezel: unit * 0.035, corner: unit * 0.14)

// Search results: one short line that says what it does, above the devices.
if kind == "search" {
    let para = NSMutableParagraphStyle()
    para.alignment = .center
    let title = NSAttributedString(string: "Share your iPhone's data", attributes: [
        .font: NSFont.systemFont(ofSize: 200, weight: .heavy),
        .foregroundColor: look.title, .paragraphStyle: para, .kern: -1.5,
    ])
    let size = title.boundingRect(with: NSSize(width: W, height: 400), options: [.usesLineFragmentOrigin])
    NSGraphicsContext.saveGraphicsState()
    let s = NSShadow()
    // A shadow helps white text on a colored backdrop; dark text needs none.
    s.shadowColor = style == "green" ? rgb(0x06381A, 0.45) : .clear
    s.shadowBlurRadius = 24
    s.shadowOffset = NSSize(width: 0, height: -6)
    s.set()
    title.draw(with: flip(CGRect(x: 0, y: 210, width: W, height: size.height)),
               options: [.usesLineFragmentOrigin])
    NSGraphicsContext.restoreGraphicsState()
}

NSGraphicsContext.restoreGraphicsState()
// App Store Connect rejects images with an alpha channel: flatten to RGB.
guard let cg = rep.cgImage,
      let flat = CGContext(data: nil, width: Int(W), height: Int(H), bitsPerComponent: 8, bytesPerRow: 0,
                           space: CGColorSpace(name: CGColorSpace.sRGB)!,
                           bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue) else { exit(1) }
flat.draw(cg, in: CGRect(x: 0, y: 0, width: W, height: H))
let png = NSBitmapImageRep(cgImage: flat.makeImage()!).representation(using: .png, properties: [:])!
try! png.write(to: URL(fileURLWithPath: out))
print(out)

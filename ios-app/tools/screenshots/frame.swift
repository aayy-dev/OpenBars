// Composes an App Store screenshot: a caption on a soft green background,
// and the raw screenshot below it in a device-like frame.
//
// usage: frame <in.png> <out.png> <width> <height> <style: phone|tablet|mac> <title> <subtitle>
import AppKit

let a = CommandLine.arguments
guard a.count == 8, let shot = NSImage(contentsOfFile: a[1]),
      let W = Double(a[3]), let H = Double(a[4]) else {
    FileHandle.standardError.write("usage: frame in.png out.png W H phone|tablet|mac title subtitle\n".data(using: .utf8)!)
    exit(2)
}
let out = a[2], style = a[5], title = a[6], subtitle = a[7]

guard let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: Int(W), pixelsHigh: Int(H),
                                 bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                                 colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0) else { exit(1) }
rep.size = NSSize(width: W, height: H) // 1 point = 1 pixel
NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
let ctx = NSGraphicsContext.current!.cgContext

func rgb(_ hex: UInt32) -> NSColor {
    NSColor(srgbRed: CGFloat((hex >> 16) & 0xFF) / 255, green: CGFloat((hex >> 8) & 0xFF) / 255,
            blue: CGFloat(hex & 0xFF) / 255, alpha: 1)
}

// Background: light green at the top fading to a deeper green.
NSGradient(colors: [rgb(0xEAF8F0), rgb(0xBFE8D2)])!.draw(in: NSRect(x: 0, y: 0, width: W, height: H), angle: -90)

// Caption, centered near the top (AppKit's y axis points up).
let unit = min(W, H * 0.75) // scale text to the canvas
let para = NSMutableParagraphStyle()
para.alignment = .center
para.lineBreakMode = .byWordWrapping
let titleAttrs: [NSAttributedString.Key: Any] = [
    .font: NSFont.systemFont(ofSize: unit * (style == "mac" ? 0.042 : 0.074), weight: .bold),
    .foregroundColor: rgb(0x0E2A1F), .paragraphStyle: para, .kern: -0.5,
]
let subAttrs: [NSAttributedString.Key: Any] = [
    .font: NSFont.systemFont(ofSize: unit * (style == "mac" ? 0.022 : 0.037), weight: .medium),
    .foregroundColor: rgb(0x2F5446), .paragraphStyle: para,
]
let textWidth = W * (style == "mac" ? 0.7 : 0.84)
let t = NSAttributedString(string: title, attributes: titleAttrs)
let s = NSAttributedString(string: subtitle, attributes: subAttrs)
let tSize = t.boundingRect(with: NSSize(width: textWidth, height: H), options: [.usesLineFragmentOrigin, .usesFontLeading])
let sSize = s.boundingRect(with: NSSize(width: textWidth, height: H), options: [.usesLineFragmentOrigin, .usesFontLeading])
let topMargin = H * (style == "mac" ? 0.06 : 0.055)
let gap = unit * 0.022
let tY = H - topMargin - tSize.height
t.draw(with: NSRect(x: (W - textWidth) / 2, y: tY, width: textWidth, height: tSize.height),
       options: [.usesLineFragmentOrigin, .usesFontLeading])
let sY = tY - gap - sSize.height
s.draw(with: NSRect(x: (W - textWidth) / 2, y: sY, width: textWidth, height: sSize.height),
       options: [.usesLineFragmentOrigin, .usesFontLeading])

// The screenshot in a frame below the caption.
let captionBottom = H - sY + unit * 0.05 // distance from the top
// Pixel size: a 2x capture reports half its pixels as its point size.
let px = shot.representations.first
let imgSize = NSSize(width: Double(px?.pixelsWide ?? Int(shot.size.width)), height: Double(px?.pixelsHigh ?? Int(shot.size.height)))
var frameW: Double, bezel: Double, corner: Double
switch style {
case "mac":
    frameW = min(W * 0.78, imgSize.width); bezel = 0; corner = 40 // native size, macOS window corners
case "tablet":
    frameW = W * 0.80; bezel = W * 0.022; corner = W * 0.035
default:
    frameW = W * 0.80; bezel = W * 0.018; corner = W * 0.115
}
let screenW = frameW - 2 * bezel
let screenH = screenW * imgSize.height / imgSize.width
let frameH = screenH + 2 * bezel
let frameX = (W - frameW) / 2
let frameTop = captionBottom
let frameY = H - frameTop - frameH // may run off the bottom, on purpose for phones

ctx.saveGState()
let shadow = NSShadow()
shadow.shadowColor = NSColor.black.withAlphaComponent(0.28)
shadow.shadowBlurRadius = unit * 0.04
shadow.shadowOffset = NSSize(width: 0, height: -unit * 0.012)
shadow.set()
let outer = NSBezierPath(roundedRect: NSRect(x: frameX, y: frameY, width: frameW, height: frameH),
                         xRadius: corner + bezel, yRadius: corner + bezel)
(style == "mac" ? NSColor.white : rgb(0x111214)).setFill()
outer.fill()
ctx.restoreGState()

let screenRect = NSRect(x: frameX + bezel, y: frameY + bezel, width: screenW, height: screenH)
NSGraphicsContext.saveGraphicsState()
NSBezierPath(roundedRect: screenRect, xRadius: corner, yRadius: corner).addClip()
shot.draw(in: screenRect, from: .zero, operation: .sourceOver, fraction: 1, respectFlipped: true,
          hints: [.interpolation: NSNumber(value: NSImageInterpolation.high.rawValue)])
NSGraphicsContext.restoreGraphicsState()

NSGraphicsContext.restoreGraphicsState()
// App Store Connect rejects images with an alpha channel: flatten to RGB.
guard let cg = rep.cgImage,
      let flat = CGContext(data: nil, width: Int(W), height: Int(H), bitsPerComponent: 8, bytesPerRow: 0,
                           space: CGColorSpace(name: CGColorSpace.sRGB)!, bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue) else { exit(1) }
flat.draw(cg, in: CGRect(x: 0, y: 0, width: W, height: H))
let png = NSBitmapImageRep(cgImage: flat.makeImage()!).representation(using: .png, properties: [:])!
try! png.write(to: URL(fileURLWithPath: out))
print(out)

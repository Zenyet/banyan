// Draws the GitBrains app icon: a "gb" monogram whose letters share one
// stem. The "g" hangs its bowl and hook off the left of the stem, the "b"
// its bowl off the right, and the stem runs from b's ascender to g's tail.
// Usage: swift scripts/make_icon.swift <output.png>
import AppKit

let size: CGFloat = 1024
let output = CommandLine.arguments.dropFirst().first ?? "icon.png"

// Draw into an exact 1024px bitmap, whatever the screen's scale.
let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: Int(size), pixelsHigh: Int(size), bitsPerSample: 8,
                           samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB,
                           bytesPerRow: 0, bitsPerPixel: 0)!
NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
let ctx = NSGraphicsContext.current!.cgContext

func color(_ hex: Int, _ alpha: CGFloat = 1) -> CGColor {
    NSColor(red: CGFloat((hex >> 16) & 0xff) / 255, green: CGFloat((hex >> 8) & 0xff) / 255,
            blue: CGFloat(hex & 0xff) / 255, alpha: alpha).cgColor
}
func gradient(_ colors: [Int]) -> CGGradient {
    CGGradient(colorsSpace: CGColorSpaceCreateDeviceRGB(), colors: colors.map { color($0) } as CFArray, locations: nil)!
}

// macOS icon grid: an 824pt rounded square centred in 1024.
let inset: CGFloat = 100
let background = CGRect(x: inset, y: inset, width: size - 2 * inset, height: size - 2 * inset)
ctx.saveGState()
ctx.addPath(CGPath(roundedRect: background, cornerWidth: 185, cornerHeight: 185, transform: nil))
ctx.clip()
ctx.drawLinearGradient(gradient([0x262a38, 0x101219]), start: CGPoint(x: 0, y: size), end: CGPoint(x: 0, y: 0), options: [])
ctx.restoreGState()

// Git's orange for the g, violet for the brains.
let orange = [0xff7a4d, 0xe0451f]
let violet = [0xa98bff, 0x6a4ae8]

let weight: CGFloat = 104 // stroke width
let radius: CGFloat = 140 // bowls, measured to the middle of the stroke
let stem: CGFloat = 512 // x of the shared stem
let middle: CGFloat = 524 // y of the bowls' centres (y grows upwards)
let top = middle + radius + 150 // b's ascender
let tail = middle - radius - 30 // where g's stem turns into its hook
let hook: CGFloat = 122

/// Fill the outline of `path`, stroked at `weight`, with a gradient from
/// `start` to `end`.
func paint(_ path: CGPath, _ colors: [Int], from start: CGPoint, to end: CGPoint, shadow: Bool = true) {
    let outline = path.copy(strokingWithWidth: weight, lineCap: .round, lineJoin: .round, miterLimit: 10)
    if shadow {
        ctx.saveGState()
        ctx.setShadow(offset: CGSize(width: 0, height: -10), blur: 28, color: color(0x000000, 0.5))
        ctx.addPath(outline)
        ctx.setFillColor(color(colors.last!))
        ctx.fillPath()
        ctx.restoreGState()
    }
    ctx.saveGState()
    ctx.addPath(outline)
    ctx.clip()
    ctx.drawLinearGradient(gradient(colors), start: start, end: end, options: [.drawsBeforeStartLocation, .drawsAfterEndLocation])
    ctx.restoreGState()
}

// The bowls.
let gBowl = CGMutablePath()
gBowl.addEllipse(in: CGRect(x: stem - 2 * radius, y: middle - radius, width: 2 * radius, height: 2 * radius))
let bBowl = CGMutablePath()
bBowl.addEllipse(in: CGRect(x: stem, y: middle - radius, width: 2 * radius, height: 2 * radius))
paint(gBowl, orange, from: CGPoint(x: 0, y: middle + radius), to: CGPoint(x: 0, y: middle - radius))
paint(bBowl, violet, from: CGPoint(x: 0, y: middle + radius), to: CGPoint(x: 0, y: middle - radius))

// The shared stem, violet up in the b's ascender and orange down in the
// g's tail, ending in the g's hook.
let spine = CGMutablePath()
spine.move(to: CGPoint(x: stem, y: top))
spine.addLine(to: CGPoint(x: stem, y: tail))
spine.addArc(center: CGPoint(x: stem - hook, y: tail), radius: hook, startAngle: 0, endAngle: -.pi * 0.95, clockwise: true)
paint(spine, [violet[0], violet[1], orange[0], orange[1]], from: CGPoint(x: 0, y: top), to: CGPoint(x: 0, y: tail - hook),
      shadow: false)

NSGraphicsContext.current = nil
try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: output))
print("wrote \(output)")

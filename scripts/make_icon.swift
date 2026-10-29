// Draws the GitBrains app icon: a forked branch, like a tree's fork or a
// commit graph. The trunk and the left branch are the main line in Git's
// orange; the right branch forks off in violet; each tip is a commit.
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
let dark = (0x262a38, 0x101219)
ctx.saveGState()
ctx.addPath(CGPath(roundedRect: background, cornerWidth: 185, cornerHeight: 185, transform: nil))
ctx.clip()
ctx.drawLinearGradient(gradient([dark.0, dark.1]), start: CGPoint(x: 0, y: size), end: CGPoint(x: 0, y: 0), options: [])
ctx.restoreGState()

let orange = [0xff7a4d, 0xe0451f]
let violet = [0xa98bff, 0x6a4ae8]

let weight: CGFloat = 92 // branch stroke width
let node: CGFloat = 78 // commit radius, to the outside of its ring
let ring: CGFloat = 40 // commit ring width

// Points, with y growing upwards.
let root = CGPoint(x: 512, y: 230)
let fork = CGPoint(x: 512, y: 520)
let left = CGPoint(x: 338, y: 790)
let right = CGPoint(x: 686, y: 790)

/// Fill the outline of `path`, stroked at `width`, with a vertical gradient.
func paint(_ path: CGPath, width: CGFloat, _ colors: [Int], from top: CGFloat, to bottom: CGFloat) {
    let outline = path.copy(strokingWithWidth: width, lineCap: .round, lineJoin: .round, miterLimit: 10)
    ctx.saveGState()
    ctx.setShadow(offset: CGSize(width: 0, height: -10), blur: 26, color: color(0x000000, 0.45))
    ctx.addPath(outline)
    ctx.setFillColor(color(colors.last!))
    ctx.fillPath()
    ctx.restoreGState()
    ctx.saveGState()
    ctx.addPath(outline)
    ctx.clip()
    ctx.drawLinearGradient(gradient(colors), start: CGPoint(x: 0, y: top), end: CGPoint(x: 0, y: bottom),
                           options: [.drawsBeforeStartLocation, .drawsAfterEndLocation])
    ctx.restoreGState()
}

/// A branch from the fork, leaving it upwards and bending out to `tip`.
func branch(to tip: CGPoint) -> CGPath {
    let path = CGMutablePath()
    path.move(to: fork)
    path.addCurve(to: tip, control1: CGPoint(x: fork.x, y: fork.y + 150), control2: CGPoint(x: tip.x, y: tip.y - 170))
    return path
}

// The new branch first, so the main line crosses over it at the fork.
paint(branch(to: right), width: weight, [violet[0], violet[1], orange[1]], from: right.y, to: fork.y - 40)
let main = CGMutablePath()
main.move(to: root)
main.addLine(to: fork)
main.addPath(branch(to: left))
paint(main, width: weight, orange, from: left.y, to: root.y)

/// The background's color at height `y`, where it blends from `dark.0`
/// at the top of the icon to `dark.1` at the bottom.
func backgroundColor(at y: CGFloat) -> CGColor {
    let t = y / size
    let channel = { (shift: Int) -> CGFloat in
        let top = CGFloat((dark.0 >> shift) & 0xff), bottom = CGFloat((dark.1 >> shift) & 0xff)
        return (bottom + (top - bottom) * t) / 255
    }
    return NSColor(red: channel(16), green: channel(8), blue: channel(0), alpha: 1).cgColor
}

/// A commit: a ring with the background showing through.
func commit(at center: CGPoint, _ colors: [Int]) {
    let circle = CGPath(ellipseIn: CGRect(x: center.x - node, y: center.y - node, width: 2 * node, height: 2 * node),
                        transform: nil)
    ctx.saveGState()
    ctx.setShadow(offset: CGSize(width: 0, height: -10), blur: 26, color: color(0x000000, 0.45))
    ctx.addPath(circle)
    ctx.setFillColor(color(colors.last!))
    ctx.fillPath()
    ctx.restoreGState()
    ctx.saveGState()
    ctx.addPath(circle)
    ctx.clip()
    ctx.drawLinearGradient(gradient(colors), start: CGPoint(x: 0, y: center.y + node), end: CGPoint(x: 0, y: center.y - node),
                           options: [])
    let hole = node - ring
    ctx.setFillColor(backgroundColor(at: center.y))
    ctx.fillEllipse(in: CGRect(x: center.x - hole, y: center.y - hole, width: 2 * hole, height: 2 * hole))
    ctx.restoreGState()
}
commit(at: root, orange)
commit(at: left, orange)
commit(at: right, violet)

NSGraphicsContext.current = nil
try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: output))
print("wrote \(output)")

// Draws the GitBrains app icon: a big bare tree with a few thick limbs
// forking against an overcast sky, as git history branches. The tree is
// grown from a fixed seed, so every run draws the same one.
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

/// A small deterministic random number generator (xorshift).
var seed: UInt64 = 11
func random(_ low: CGFloat, _ high: CGFloat) -> CGFloat {
    seed ^= seed << 13
    seed ^= seed >> 7
    seed ^= seed << 17
    return low + (high - low) * CGFloat(seed % 1_000_000) / 1_000_000
}

// macOS icon grid: an 824pt rounded square centred in 1024.
let inset: CGFloat = 100
let tile = CGRect(x: inset, y: inset, width: size - 2 * inset, height: size - 2 * inset)
ctx.addPath(CGPath(roundedRect: tile, cornerWidth: 185, cornerHeight: 185, transform: nil))
ctx.clip()

// An overcast sky, brightest where the sun is behind the clouds.
ctx.drawLinearGradient(gradient([0xe9ecef, 0xb9c0c8, 0x8c949e]), start: CGPoint(x: 0, y: tile.maxY),
                       end: CGPoint(x: 0, y: tile.minY), options: [])
let glow = CGGradient(colorsSpace: CGColorSpaceCreateDeviceRGB(),
                      colors: [color(0xffffff, 0.75), color(0xffffff, 0)] as CFArray, locations: [0, 1])!
ctx.drawRadialGradient(glow, startCenter: CGPoint(x: 690, y: 760), startRadius: 0,
                       endCenter: CGPoint(x: 690, y: 760), endRadius: 420, options: [])

// A soft shadow where the trunk meets the ground.
let shadow = CGGradient(colorsSpace: CGColorSpaceCreateDeviceRGB(),
                        colors: [color(0x2a2522, 0.55), color(0x2a2522, 0)] as CFArray, locations: [0, 1])!
ctx.saveGState()
ctx.translateBy(x: 512, y: 96)
ctx.scaleBy(x: 2.6, y: 0.5)
ctx.drawRadialGradient(shadow, startCenter: .zero, startRadius: 0, endCenter: .zero, endRadius: 120, options: [])
ctx.restoreGState()

/// One tapered piece of a branch, from `a` (width `wa`) to `b` (width `wb`),
/// with round ends so consecutive pieces join smoothly.
func segment(_ a: CGPoint, _ b: CGPoint, _ wa: CGFloat, _ wb: CGFloat) {
    let angle = atan2(b.y - a.y, b.x - a.x) + .pi / 2
    let (dx, dy) = (cos(angle), sin(angle))
    let path = CGMutablePath()
    path.move(to: CGPoint(x: a.x + dx * wa / 2, y: a.y + dy * wa / 2))
    path.addLine(to: CGPoint(x: b.x + dx * wb / 2, y: b.y + dy * wb / 2))
    path.addLine(to: CGPoint(x: b.x - dx * wb / 2, y: b.y - dy * wb / 2))
    path.addLine(to: CGPoint(x: a.x - dx * wa / 2, y: a.y - dy * wa / 2))
    path.closeSubpath()
    ctx.addPath(path)
    ctx.fillPath()
    ctx.fillEllipse(in: CGRect(x: b.x - wb / 2, y: b.y - wb / 2, width: wb, height: wb))
}

/// Grow a branch from `start` heading `angle` (radians, π/2 is up), then
/// fork it a few times; the tips stay thick and rounded.
func grow(from start: CGPoint, angle: CGFloat, length: CGFloat, width: CGFloat, depth: Int) {
    let steps = 5
    var point = start
    var heading = angle
    var w = width
    let end = width * random(0.74, 0.82)
    for step in 0..<steps {
        // Wander a little, and lean back towards the light above.
        heading += random(-0.09, 0.09)
        heading += (CGFloat.pi / 2 - heading) * 0.06
        let next = CGPoint(x: point.x + cos(heading) * length / CGFloat(steps),
                           y: point.y + sin(heading) * length / CGFloat(steps))
        let nextWidth = width + (end - width) * CGFloat(step + 1) / CGFloat(steps)
        segment(point, next, w, nextWidth)
        point = next
        w = nextWidth
    }
    guard w > 12, depth < 4 else { return }
    // Fork in two, now and then three; one fork carries on thicker.
    let forks = random(0, 1) < 0.15 ? 3 : 2
    let spread = random(0.45, 0.7)
    for i in 0..<forks {
        let offset = forks == 2 ? (i == 0 ? -spread : spread) : CGFloat(i - 1) * spread
        let lead = i == (forks == 2 ? Int(random(0, 2)) : 1)
        grow(from: point, angle: heading + offset + random(-0.12, 0.12),
             length: length * random(0.7, 0.8),
             width: w * (lead ? random(0.8, 0.9) : random(0.66, 0.78)),
             depth: depth + 1)
    }
}

// The tree as one silhouette, then lit from the left: warm bark in the
// light, dark on the shaded side.
ctx.beginTransparencyLayer(auxiliaryInfo: nil)
ctx.setFillColor(color(0x000000))
// The trunk rises from below the bottom edge.
grow(from: CGPoint(x: 512, y: 60), angle: .pi / 2, length: 250, width: 104, depth: 0)
ctx.setBlendMode(.sourceIn)
ctx.drawLinearGradient(gradient([0x5c473a, 0x31251f, 0x17110e]), start: CGPoint(x: tile.minX + 120, y: 0),
                       end: CGPoint(x: tile.maxX - 60, y: 0), options: [.drawsBeforeStartLocation, .drawsAfterEndLocation])
ctx.endTransparencyLayer()

NSGraphicsContext.current = nil
try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: output))
print("wrote \(output)")

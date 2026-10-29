// Draws the GitBrains app icon: a bare tree branching irregularly against
// an overcast sky, as git history branches. The tree is grown from a fixed
// seed, so every run draws the same one.
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
var seed: UInt64 = 42
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

let bark = color(0x1d1a1c)
ctx.setFillColor(bark)

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
/// fork it, until the twigs are too thin to see.
func grow(from start: CGPoint, angle: CGFloat, length: CGFloat, width: CGFloat, depth: Int) {
    let steps = 5
    var point = start
    var heading = angle
    var w = width
    let end = width * random(0.58, 0.7)
    for step in 0..<steps {
        // Wander a little, and lean back towards the light above.
        heading += random(-0.16, 0.16)
        heading += (CGFloat.pi / 2 - heading) * 0.06
        let next = CGPoint(x: point.x + cos(heading) * length / CGFloat(steps),
                           y: point.y + sin(heading) * length / CGFloat(steps))
        let nextWidth = width + (end - width) * CGFloat(step + 1) / CGFloat(steps)
        segment(point, next, w, nextWidth)
        // Now and then a twig sprouts on the way.
        if depth >= 2 && nextWidth > 1.6 && random(0, 1) < 0.5 {
            let side: CGFloat = random(0, 1) < 0.5 ? -1 : 1
            grow(from: next, angle: heading + side * random(0.5, 0.9), length: length * random(0.3, 0.45),
                 width: nextWidth * random(0.4, 0.55), depth: depth + 2)
        }
        point = next
        w = nextWidth
    }
    guard w > 1.1, depth < 10 else { return }
    // Fork in two, sometimes three; one fork usually carries on thicker.
    let forks = random(0, 1) < 0.3 ? 3 : 2
    let spread = random(0.32, 0.58)
    for i in 0..<forks {
        let offset = forks == 2 ? (i == 0 ? -spread : spread) : CGFloat(i - 1) * spread
        let lead = i == (forks == 2 ? Int(random(0, 2)) : 1)
        grow(from: point, angle: heading + offset + random(-0.12, 0.12),
             length: length * random(0.68, 0.8),
             width: w * (lead ? random(0.78, 0.9) : random(0.58, 0.72)),
             depth: depth + 1)
    }
}

// The trunk rises from below the bottom edge.
grow(from: CGPoint(x: 506, y: 60), angle: .pi / 2, length: 250, width: 72, depth: 0)

NSGraphicsContext.current = nil
try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: output))
print("wrote \(output)")

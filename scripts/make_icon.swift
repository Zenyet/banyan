// Draws the GitBrains app icon: "brains" across and "git" down, crossing at
// the "i" they share, each letter on its own tile like a crossword.
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
func gradient(_ a: Int, _ b: Int) -> CGGradient {
    CGGradient(colorsSpace: CGColorSpaceCreateDeviceRGB(), colors: [color(a), color(b)] as CFArray, locations: [0, 1])!
}

// macOS icon grid: an 824pt rounded square centred in 1024.
let inset: CGFloat = 100
let background = CGRect(x: inset, y: inset, width: size - 2 * inset, height: size - 2 * inset)
ctx.saveGState()
ctx.addPath(CGPath(roundedRect: background, cornerWidth: 185, cornerHeight: 185, transform: nil))
ctx.clip()
ctx.drawLinearGradient(gradient(0x262a38, 0x101219), start: CGPoint(x: 0, y: size), end: CGPoint(x: 0, y: 0), options: [])
ctx.restoreGState()

// Git's orange and a violet for the brains.
let git = (0xf4683f, 0xd8401f)
let brains = (0x9b7bff, 0x6a4ae8)

let cell: CGFloat = 120, gap: CGFloat = 9, radius: CGFloat = 26
let across = Array("brains"), down = Array("git")
let shared = 3 // the "i" of "brains", which is also the "i" of "git"
let width = CGFloat(across.count) * cell + CGFloat(across.count - 1) * gap
let left = (size - width) / 2
let middle = size / 2 - cell / 2 // bottom of the "brains" row

let font: NSFont = {
    let base = NSFont.systemFont(ofSize: cell * 0.64, weight: .heavy)
    let rounded = base.fontDescriptor.withDesign(.rounded) ?? base.fontDescriptor
    return NSFont(descriptor: rounded, size: cell * 0.64) ?? base
}()

func tile(_ letter: Character, at origin: CGPoint, colors: (Int, Int), split: (Int, Int)? = nil) {
    let rect = CGRect(origin: origin, size: CGSize(width: cell, height: cell))
    let path = CGPath(roundedRect: rect, cornerWidth: radius, cornerHeight: radius, transform: nil)
    // Soft shadow under each tile.
    ctx.saveGState()
    ctx.setShadow(offset: CGSize(width: 0, height: -6), blur: 14, color: color(0x000000, 0.45))
    ctx.addPath(path)
    ctx.setFillColor(color(colors.1))
    ctx.fillPath()
    ctx.restoreGState()

    ctx.saveGState()
    ctx.addPath(path)
    ctx.clip()
    if let split {
        // The shared tile blends from git (top left) into brains (bottom right).
        ctx.drawLinearGradient(gradient(colors.0, split.1), start: CGPoint(x: rect.minX, y: rect.maxY),
                               end: CGPoint(x: rect.maxX, y: rect.minY), options: [])
    } else {
        ctx.drawLinearGradient(gradient(colors.0, colors.1), start: CGPoint(x: 0, y: rect.maxY), end: CGPoint(x: 0, y: rect.minY), options: [])
    }
    // A thin highlight along the top edge.
    ctx.setFillColor(color(0xffffff, 0.14))
    ctx.fill(CGRect(x: rect.minX, y: rect.maxY - 5, width: rect.width, height: 5))
    ctx.restoreGState()

    // Centre the glyph's ink, not its line box, so "g" and "b" sit level.
    let text = NSAttributedString(string: String(letter), attributes: [.font: font, .foregroundColor: NSColor.white])
    let line = CTLineCreateWithAttributedString(text)
    ctx.textPosition = .zero
    let ink = CTLineGetImageBounds(line, ctx)
    ctx.textPosition = CGPoint(x: rect.midX - ink.midX, y: rect.midY - ink.midY)
    CTLineDraw(line, ctx)
}

let x = { (column: Int) in left + CGFloat(column) * (cell + gap) }
for (column, letter) in across.enumerated() where column != shared {
    tile(letter, at: CGPoint(x: x(column), y: middle), colors: brains)
}
// "g" above the shared tile and "t" below it.
tile(down[0], at: CGPoint(x: x(shared), y: middle + cell + gap), colors: git)
tile(down[2], at: CGPoint(x: x(shared), y: middle - cell - gap), colors: git)
tile(down[1], at: CGPoint(x: x(shared), y: middle), colors: git, split: brains)

NSGraphicsContext.current = nil
try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: output))
print("wrote \(output)")

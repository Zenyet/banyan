// Draws the GitBrains app icon: a commit graph on a dark rounded square.
// Usage: swift scripts/make_icon.swift <output.png>
import AppKit

let size: CGFloat = 1024
let output = CommandLine.arguments.dropFirst().first ?? "icon.png"

let image = NSImage(size: NSSize(width: size, height: size))
image.lockFocus()
let ctx = NSGraphicsContext.current!.cgContext

// macOS icon grid: an 824pt rounded square centred in 1024.
let inset: CGFloat = 100
let tile = CGRect(x: inset, y: inset, width: size - 2 * inset, height: size - 2 * inset)
let tilePath = CGPath(roundedRect: tile, cornerWidth: 185, cornerHeight: 185, transform: nil)
ctx.addPath(tilePath)
ctx.clip()
let gradient = CGGradient(
    colorsSpace: CGColorSpaceCreateDeviceRGB(),
    colors: [NSColor(red: 0.16, green: 0.18, blue: 0.24, alpha: 1).cgColor,
             NSColor(red: 0.07, green: 0.08, blue: 0.11, alpha: 1).cgColor] as CFArray,
    locations: [0, 1])!
ctx.drawLinearGradient(gradient, start: CGPoint(x: 0, y: size), end: CGPoint(x: 0, y: 0), options: [])

func color(_ hex: Int) -> CGColor {
    NSColor(red: CGFloat((hex >> 16) & 0xff) / 255, green: CGFloat((hex >> 8) & 0xff) / 255,
            blue: CGFloat(hex & 0xff) / 255, alpha: 1).cgColor
}
let blue = color(0x4d8ee8), orange = color(0xe0a33d), green = color(0x58b25a) // green: the merge

let left: CGFloat = 380, right: CGFloat = 640, top: CGFloat = 800, bottom: CGFloat = 220
ctx.setLineWidth(46)
ctx.setLineCap(.round)

func stroke(_ c: CGColor, _ build: (CGMutablePath) -> Void) {
    let path = CGMutablePath()
    build(path)
    ctx.setStrokeColor(c)
    ctx.addPath(path)
    ctx.strokePath()
}
// Main line.
stroke(blue) { $0.move(to: CGPoint(x: left, y: top)); $0.addLine(to: CGPoint(x: left, y: bottom)) }
// A branch that leaves and merges back.
stroke(orange) {
    $0.move(to: CGPoint(x: left, y: 690))
    $0.addCurve(to: CGPoint(x: right, y: 590), control1: CGPoint(x: left, y: 630), control2: CGPoint(x: right, y: 650))
    $0.addLine(to: CGPoint(x: right, y: 450))
    $0.addCurve(to: CGPoint(x: left, y: 340), control1: CGPoint(x: right, y: 390), control2: CGPoint(x: left, y: 400))
}

func node(_ x: CGFloat, _ y: CGFloat, _ c: CGColor, hollow: Bool = false) {
    let r: CGFloat = 52
    let rect = CGRect(x: x - r, y: y - r, width: 2 * r, height: 2 * r)
    ctx.setFillColor(c)
    ctx.fillEllipse(in: rect)
    if hollow {
        ctx.setFillColor(color(0x1a1d26))
        ctx.fillEllipse(in: rect.insetBy(dx: 24, dy: 24))
    }
}
node(left, top, blue, hollow: true)
node(right, 590, orange)
node(right, 450, orange)
node(left, 340, green)
node(left, bottom, blue)

image.unlockFocus()
let rep = NSBitmapImageRep(data: image.tiffRepresentation!)!
try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: output))
print("wrote \(output)")

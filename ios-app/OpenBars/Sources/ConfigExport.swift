import CoreImage
import SwiftUI

/// A WireGuard config (or a link) as a QR code, drawn with sharp modules on
/// white so any camera can read it in light or dark mode.
struct ConfigQRCode: View {
    let config: String
    var label = "QR code of the device's configuration"
    var maxSide: CGFloat = 280

    var body: some View {
        if let image = Self.render(config) {
            Image(decorative: image, scale: 1)
                .interpolation(.none) // keep modules crisp when scaled
                .resizable()
                .aspectRatio(1, contentMode: .fit)
                .frame(maxWidth: maxSide)
                .padding(14)
                .background(.white, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                .frame(maxWidth: .infinity)
                .accessibilityLabel(label)
        }
    }

    private static let context = CIContext(options: [.useSoftwareRenderer: false])

    /// One pixel per module; SwiftUI scales it up without smoothing.
    static func render(_ text: String) -> CGImage? {
        guard let filter = CIFilter(name: "CIQRCodeGenerator") else { return nil }
        filter.setValue(Data(text.utf8), forKey: "inputMessage")
        filter.setValue("M", forKey: "inputCorrectionLevel") // ~15% damage tolerance
        guard let output = filter.outputImage else { return nil }
        return context.createCGImage(output, from: output.extent.integral)
    }
}

enum ConfigFile {
    /// Writes the config to a temporary .conf named after the device, for
    /// sharing. WireGuard apps use the file name as the tunnel name and
    /// accept letters, digits and `_=+.-`, up to 32 characters.
    static func write(_ config: String, deviceName: String) -> URL? {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent("configs", isDirectory: true)
        let file = folder.appendingPathComponent(tunnelName(deviceName)).appendingPathExtension("conf")
        do {
            try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
            try Data(config.utf8).write(to: file, options: .atomic)
            return file
        } catch {
            return nil
        }
    }

    static func tunnelName(_ deviceName: String) -> String {
        let allowed = CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_=+.-")
        var name = ""
        for scalar in deviceName.unicodeScalars {
            if allowed.contains(scalar) {
                name.unicodeScalars.append(scalar)
            } else if !name.hasSuffix("-") {
                name += "-"
            }
        }
        name = name.trimmingCharacters(in: CharacterSet(charactersIn: "-."))
        return name.isEmpty ? "OpenBars" : String(name.prefix(32))
    }
}

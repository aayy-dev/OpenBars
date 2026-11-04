import SwiftUI
import UserNotifications

// MARK: - A device's limits (Device detail)

/// Pause, limits with how much is used, what happens at a limit, giving
/// more, and resetting.
struct DeviceLimitSection: View {
    @EnvironmentObject var store: Store
    let device: Device
    @State private var addingRule = false
    @State private var confirmReset = false

    var body: some View {
        let limit = device.limit ?? DeviceLimit()
        let status = store.limitStatus(for: device)
        Section {
            Toggle(isOn: Binding(get: { limit.paused }, set: { store.setPaused($0, device) })) {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Pause Internet")
                    if limit.paused {
                        Text("It can't use your connection until you turn this off.").font(.caption).foregroundStyle(.secondary)
                    }
                }
            }
            ForEach(limit.rules) { rule in
                RuleUsageRow(rule: rule, status: status?.rules?.first { $0.period == rule.period.rawValue && $0.bytes == rule.bytes })
            }
            .onDelete { idx in
                for i in idx { store.removeRule(limit.rules[i], from: device) }
            }
            Button {
                addingRule = true
            } label: {
                Label(limit.rules.isEmpty ? "Add a Data Limit" : "Add Another Limit", systemImage: "plus.circle")
            }
            if !limit.rules.isEmpty {
                Picker("When It's Reached", selection: Binding(
                    get: { limit.action },
                    set: { store.setAction($0, slowKbps: limit.slowKbps ?? store.state.settings.slowKbps, for: device) })
                ) {
                    Text("Default (\(store.state.settings.limitAction.title))").tag(LimitAction?.none)
                    ForEach(LimitAction.allCases) { Text($0.title).tag(LimitAction?.some($0)) }
                }
                if (limit.action ?? store.state.settings.limitAction) == .slow {
                    Picker("Slow Down To", selection: Binding(
                        get: { limit.slowKbps ?? store.state.settings.slowKbps },
                        set: { store.setAction(.slow, slowKbps: $0, for: device) })
                    ) {
                        ForEach(slowSpeeds, id: \.self) { Text(speedTitle($0)).tag($0) }
                    }
                }
                giveMoreMenu
                ForEach((status?.grants ?? []).filter(\.active), id: \.id) { g in
                    Label(grantText(g), systemImage: "gift").font(.subheadline).foregroundStyle(.secondary)
                }
                Button("Reset Usage") { confirmReset = true }
            }
        } header: {
            Text("Data Limit")
        } footer: {
            Text(footer(limit: limit, status: status))
        }
        .sheet(isPresented: $addingRule) {
            LimitRuleEditor(title: "Data Limit for “\(device.name)”") { store.addRule($0, to: device) }
        }
        .confirmationDialog("Reset “\(device.name)”'s usage?", isPresented: $confirmReset, titleVisibility: .visible) {
            Button("Reset Usage", role: .destructive) { store.resetUsage(device) }
        } message: {
            Text("Data it used so far stops counting toward its limits. Its usage history stays.")
        }
    }

    private var giveMoreMenu: some View {
        Menu {
            Button("15 More Minutes") { give("15m") }
            Button("1 More Hour") { give("1h") }
            Button("No Limit Until Midnight") { give("today") }
            Button("250 MB More") { give("250mb") }
            Button("1 GB More") { give("1gb") }
        } label: {
            Label("Give More", systemImage: "gift")
        }
    }

    private func give(_ id: String) {
        if let g = Store.quickGrant(id) { store.give(g, to: device) }
    }

    private func grantText(_ g: GrantStatus) -> String {
        if let u = g.until, u > 0 {
            return "No limit until " + Date(timeIntervalSince1970: TimeInterval(u)).formatted(date: .omitted, time: .shortened)
        }
        return ByteCountFormatter.string(fromByteCount: g.left ?? 0, countStyle: .decimal) + " of extra data left"
    }

    private func footer(limit: DeviceLimit, status: LimitStatus?) -> String {
        var parts: [String] = []
        if status?.cap == true {
            parts.append("All your devices together reached their limit (Settings › Data Limits).")
        }
        if limit.rules.isEmpty {
            parts.append("No limit: it can use as much data as it wants.")
        } else {
            parts.append("Counts the data it uses through Open Bars on this iPhone. It can see its usage and ask for more at http://10.66.66.1 while connected.")
        }
        parts.append("If you remove this device, its limits and usage are kept, so pairing again doesn't start over.")
        return parts.joined(separator: " ")
    }
}

/// One limit with a bar of how much is used.
struct RuleUsageRow: View {
    let rule: LimitRule
    let status: RuleStatus?

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text(rule.summary.prefix(1).uppercased() + rule.summary.dropFirst())
                Spacer()
                if let s = status {
                    Text(bytes(s.used) + " used").font(.subheadline).foregroundStyle(s.isOver ? .red : .secondary).monospacedDigit()
                }
            }
            if let s = status {
                ProgressView(value: s.fraction).tint(s.isOver ? .red : (s.fraction >= 0.8 ? .orange : .green))
                Text(resetText(s)).font(.caption).foregroundStyle(.secondary)
            } else {
                Text(rule.period.resetNote).font(.caption).foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 2)
    }

    private func bytes(_ n: Int64) -> String { ByteCountFormatter.string(fromByteCount: n, countStyle: .decimal) }

    private func resetText(_ s: RuleStatus) -> String {
        guard s.resets > 0 else { return "Never starts over by itself." }
        let d = Date(timeIntervalSince1970: TimeInterval(s.resets))
        if Calendar.current.isDateInToday(d) { return "Starts over at " + d.formatted(date: .omitted, time: .shortened) + "." }
        if Calendar.current.isDateInTomorrow(d) && Calendar.current.component(.hour, from: d) == 0 { return "Starts over at midnight." }
        return "Starts over " + d.formatted(.dateTime.weekday(.wide).month().day()) + "."
    }
}

// MARK: - Adding a limit

/// Pick a period and an amount.
struct LimitRuleEditor: View {
    @Environment(\.dismiss) private var dismiss
    var title: String
    var save: (LimitRule) -> Void

    @State private var period: LimitPeriod = .day
    @State private var amount = "1"
    @State private var unit: Int64 = 1_000_000_000

    private var bytes: Int64? {
        guard let v = Double(amount.replacingOccurrences(of: ",", with: ".")), v > 0 else { return nil }
        return Int64(v * Double(unit))
    }

    var body: some View {
        NavStack {
            Form {
                Section {
                    Picker("Period", selection: $period) {
                        ForEach(LimitPeriod.allCases) { Text($0.title).tag($0) }
                    }
                } footer: {
                    Text(period.resetNote)
                }
                Section("Amount") {
                    HStack {
                        TextField("Amount", text: $amount)
                            .keyboardType(.decimalPad)
                            .font(.title3.monospacedDigit())
                        Picker("Unit", selection: $unit) {
                            Text("MB").tag(Int64(1_000_000))
                            Text("GB").tag(Int64(1_000_000_000))
                        }
                        .pickerStyle(.segmented)
                        .frame(width: 120)
                    }
                    ForEach(presets(for: period), id: \.self) { p in
                        Button(ByteCountFormatter.string(fromByteCount: p, countStyle: .decimal)) { set(p) }
                    }
                }
            }
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel", systemImage: "xmark") { dismiss() }
                }
                ToolbarItem(placement: .pinnedTrailing) {
                    Button("Add", systemImage: "checkmark") {
                        if let b = bytes { save(LimitRule(period: period, bytes: b)) }
                        dismiss()
                    }
                    .disabled(bytes == nil)
                }
            }
        }
    }

    private func set(_ b: Int64) {
        if b >= 1_000_000_000 {
            unit = 1_000_000_000
            amount = String(format: "%g", Double(b) / 1e9)
        } else {
            unit = 1_000_000
            amount = String(b / 1_000_000)
        }
    }

    private func presets(for p: LimitPeriod) -> [Int64] {
        switch p {
        case .hour: return [100_000_000, 250_000_000, 500_000_000, 1_000_000_000]
        case .day: return [500_000_000, 1_000_000_000, 2_000_000_000, 5_000_000_000]
        case .week: return [2_000_000_000, 5_000_000_000, 10_000_000_000, 20_000_000_000]
        case .month, .total: return [5_000_000_000, 10_000_000_000, 20_000_000_000, 50_000_000_000]
        }
    }
}

// MARK: - Settings › Data Limits

/// A list of limits with add and delete (new devices, all devices).
struct RulesList: View {
    @Binding var rules: [LimitRule]
    var title: String
    var empty: String
    @State private var adding = false

    var body: some View {
        ForEach(rules) { r in
            Text(r.summary.prefix(1).uppercased() + r.summary.dropFirst())
        }
        .onDelete { rules.remove(atOffsets: $0) }
        if rules.isEmpty {
            Text(empty).foregroundStyle(.secondary)
        }
        Button {
            adding = true
        } label: {
            Label("Add a Limit", systemImage: "plus.circle")
        }
        .sheet(isPresented: $adding) {
            LimitRuleEditor(title: title) { r in
                rules.removeAll { $0.period == r.period }
                rules.append(r)
            }
        }
    }
}

/// The app-wide limit settings, edited in a copy of AppSettings.
struct LimitSettingsForm: View {
    @Binding var settings: AppSettings

    var body: some View {
        Form { sections }
            .navigationTitle("Data Limits")
            .navigationBarTitleDisplayMode(.inline)
    }

    @ViewBuilder var sections: some View {
        Section {
            Picker("When a Device Reaches a Limit", selection: $settings.limitAction) {
                ForEach(LimitAction.allCases) { Text($0.title).tag($0) }
            }
            if settings.limitAction == .slow || settings.capAction == .slow {
                Picker("Slow Down To", selection: $settings.slowKbps) {
                    ForEach(slowSpeeds, id: \.self) { Text(speedTitle($0)).tag($0) }
                }
            }
        } footer: {
            Text(settings.limitAction.detail + " Each device can have its own choice too. The device can always open http://10.66.66.1 to see its usage and ask you for more.")
        }
        Section {
            RulesList(rules: $settings.newDeviceRules, title: "Limit for New Devices", empty: "No limit")
        } header: {
            Text("New Devices")
        } footer: {
            Text("The limits a device gets when you allow it. You can change them when you tap Allow, and on each device later.")
        }
        Section {
            RulesList(rules: $settings.capRules, title: "Limit for All Devices", empty: "No limit")
            if !settings.capRules.isEmpty {
                Picker("When It's Reached", selection: $settings.capAction) {
                    ForEach(LimitAction.allCases) { Text($0.title).tag($0) }
                }
            }
        } header: {
            Text("All Devices Together")
        } footer: {
            Text("Protects your plan: counts every device's data together. When it's reached, it applies to every device.")
        }
        Section {
            Picker("Billing Day", selection: $settings.billingDay) {
                ForEach(1...31, id: \.self) { Text(ordinal($0)).tag($0) }
            }
        } footer: {
            Text("Monthly limits start over on this day, like your phone plan. In shorter months, on the last day.")
        }
        Section {
            Text("Limits count the data your devices use through Open Bars. A device that knows your hotspot password can still turn its VPN off and use your Personal Hotspot directly, which Open Bars can't see or limit.")
                .font(.footnote).foregroundStyle(.secondary)
        }
    }

    private func ordinal(_ n: Int) -> String {
        let f = NumberFormatter()
        f.numberStyle = .ordinal
        return f.string(from: NSNumber(value: n)) ?? "\(n)"
    }
}

/// Data Limits on their own (from the Share tab), with Save and Cancel.
struct LimitSettingsSheet: View {
    @EnvironmentObject var store: Store
    @Environment(\.dismiss) private var dismiss
    @State var settings: AppSettings

    var body: some View {
        NavStack {
            Form { LimitSettingsForm(settings: $settings).sections }
                .navigationTitle("Data Limits")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Cancel", systemImage: "xmark") { dismiss() }
                    }
                    ToolbarItem(placement: .pinnedTrailing) {
                        Button("Save", systemImage: "checkmark") {
                            store.updateSettings(settings)
                            dismiss()
                        }
                    }
                }
        }
    }
}

// MARK: - Settings › Notifications

struct NotificationSettingsSection: View {
    @Binding var prefs: NotificationPrefs
    @State private var allowed: Bool?

    var body: some View {
        Section {
            if allowed == false {
                Button("Turn On Notifications in Settings") {
                    if let url = URL(string: UIApplication.openSettingsURLString) { UIApplication.shared.open(url) }
                }
            }
            Toggle("Device asks to connect", isOn: $prefs.pairing)
            Toggle("Data limits", isOn: $prefs.limits)
            Toggle("Device asks for more", isOn: $prefs.asks)
            Toggle("Sharing stopped by itself", isOn: $prefs.stopped)
            Toggle("Problems", isOn: $prefs.errors)
            Toggle("Sharing paused (in the app)", isOn: $prefs.paused)
        } header: {
            Text("Notifications")
        } footer: {
            Text(allowed == false
                 ? "Notifications are off for Open Bars in iOS Settings."
                 : "Most of these come while sharing in the background. You can answer from the notification: Allow, Give More, Approve.")
        }
        .onAppear {
            UNUserNotificationCenter.current().getNotificationSettings { s in
                let ok = s.authorizationStatus == .authorized || s.authorizationStatus == .provisional
                    || s.authorizationStatus == .notDetermined
                DispatchQueue.main.async { allowed = ok }
            }
        }
    }
}

// MARK: - Allowing a device

/// Allow or Don't Allow a pairing device, choosing its data limit.
struct PairingSheet: View {
    @EnvironmentObject var store: Store
    let request: PairRequest

    private enum Choice: Hashable { case none, earlier, lookalike, newDefault, preset(Int) }
    @State private var choice: Choice?

    private static let presets: [LimitRule] = [
        LimitRule(period: .day, bytes: 500_000_000),
        LimitRule(period: .day, bytes: 1_000_000_000),
        LimitRule(period: .day, bytes: 3_000_000_000),
        LimitRule(period: .month, bytes: 10_000_000_000),
    ]

    var body: some View {
        let earlier = store.removedRecord(for: request.publicKey)
        let lookalike = store.lookalike(for: request)
        let newRules = store.state.settings.newDeviceRules
        NavStack {
            Form {
                Section {
                    HStack(spacing: 14) {
                        Image(systemName: (DeviceKind(rawValue: request.kind) ?? .other).symbol)
                            .font(.system(size: 28))
                            .frame(width: 52, height: 52)
                            .background(RoundedRectangle(cornerRadius: 12, style: .continuous).fill(Color.secondary.opacity(0.12)))
                        VStack(alignment: .leading, spacing: 3) {
                            Text("“\(request.name)” wants to connect").font(.headline)
                            Text("From \(request.from) on your hotspot").font(.subheadline).foregroundStyle(.secondary)
                        }
                    }
                    .padding(.vertical, 4)
                } footer: {
                    Text("It wants to use this iPhone's connection through Open Bars. Only allow devices you know.")
                }
                Section {
                    Picker("Data Limit", selection: Binding(get: { choice ?? defaultChoice(earlier, lookalike, newRules) }, set: { choice = $0 })) {
                        if let e = earlier {
                            Text("Its earlier limits: \(summary(e.limit))").tag(Choice.earlier)
                        }
                        if let l = lookalike {
                            Text("Like “\(l.name)”: \(summary(l.limit))").tag(Choice.lookalike)
                        }
                        Text("No limit").tag(Choice.none)
                        if !newRules.isEmpty {
                            Text("New-device limit: \(newRules.map(\.summary).joined(separator: ", "))").tag(Choice.newDefault)
                        }
                        ForEach(Self.presets.indices, id: \.self) { i in
                            Text(Self.presets[i].summary).tag(Choice.preset(i))
                        }
                    }
                    .pickerStyle(.inline)
                    .labelsHidden()
                } header: {
                    Text("Data Limit")
                } footer: {
                    if earlier != nil {
                        Text("You removed this device before. Its limits and usage carry on.")
                    } else if let l = lookalike, store.state.devices.contains(where: { $0.publicKey == l.publicKey }) {
                        Text("“\(l.name)” is already connected with its own key, so this may be a different device using the same name. Choosing “Like” shares its limits and today's usage.")
                    } else if let l = lookalike {
                        Text("A device named “\(l.name)” that you removed had limits. If this is the same device with a new key, “Like” keeps its limits and counts its earlier usage, so a new key doesn't start over. A device can say any name, so check it's really the one you think.")
                    } else {
                        Text("You can change it later on the device.")
                    }
                }
                Section {
                    Button {
                        store.approvePairing(request, limit: limit(for: choice ?? defaultChoice(earlier, lookalike, newRules), earlier, lookalike))
                    } label: {
                        Text("Allow").fontWeight(.semibold).frame(maxWidth: .infinity)
                    }
                    Button("Don't Allow", role: .destructive) { store.denyPairing(request) }
                        .frame(maxWidth: .infinity)
                }
            }
            .navigationTitle("Allow Device?")
            .navigationBarTitleDisplayMode(.inline)
        }
    }

    private func defaultChoice(_ earlier: Device?, _ lookalike: Device?, _ newRules: [LimitRule]) -> Choice {
        if earlier != nil { return .earlier }
        if lookalike != nil { return .lookalike }
        return newRules.isEmpty ? .none : .newDefault
    }

    private func summary(_ l: DeviceLimit?) -> String {
        guard let l, !l.rules.isEmpty else { return "no limit" }
        return l.rules.map(\.summary).joined(separator: ", ")
    }

    private func limit(for c: Choice, _ earlier: Device?, _ lookalike: Device?) -> DeviceLimit? {
        switch c {
        case .none: return DeviceLimit()
        case .earlier: return earlier?.limit ?? DeviceLimit()
        case .lookalike:
            return lookalike.map(store.limitLike) ?? DeviceLimit()
        case .newDefault: return store.newDeviceLimit() ?? DeviceLimit()
        case .preset(let i): return DeviceLimit(rules: [Store.starting(Self.presets[i])])
        }
    }
}

// MARK: - Requests on the Share tab

/// A device asking for more time or data, with Approve and Decline.
struct AskRow: View {
    @EnvironmentObject var store: Store
    let ask: AskRequest

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Label {
                VStack(alignment: .leading, spacing: 2) {
                    Text("“\(ask.name)” asks for \(ask.what)").font(.subheadline.weight(.semibold))
                    if let m = ask.message, !m.isEmpty {
                        Text("“\(m)”").font(.subheadline)
                    }
                    Text(formatAgo(ask.since)).font(.caption).foregroundStyle(.secondary)
                }
            } icon: {
                Image(systemName: "hand.raised.fill").foregroundStyle(.orange)
            }
            HStack {
                Button("Decline", role: .destructive) { store.denyAsk(ask) }
                    .buttonStyle(.bordered)
                Button("Approve") { store.approveAsk(ask) }
                    .buttonStyle(.borderedProminent)
            }
            .controlSize(.small)
        }
        .padding(.vertical, 4)
    }
}

/// Once, for people who already use Open Bars: what data limits are.
struct LimitsIntroCard: View {
    var setUp: () -> Void
    var dismiss: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Label("New: Data Limits", systemImage: "speedometer")
                .font(.subheadline.weight(.semibold))
            Text("Set how much data each device can use per hour, day, week or month, or for all devices together. Devices can ask you for more.")
                .font(.subheadline).foregroundStyle(.secondary)
            HStack {
                Button("Not Now", action: dismiss).buttonStyle(.bordered)
                Button("Set Up", action: setUp).buttonStyle(.borderedProminent)
            }
            .controlSize(.small)
        }
        .padding(.vertical, 4)
    }
}

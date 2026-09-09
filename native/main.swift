import AppKit
import ApplicationServices
import Darwin

struct Request: Decodable { let operation: String; let id: String; let text: String?; let checked: Bool?; let participants: [String]?; let replacement: String? }
struct Item: Codable, Equatable { let text: String; let checked: Bool; let location: Int; let length: Int }
struct Failure: Error { let message: String }
var wrote = false
func fail(_ message: String) throws -> Never { throw Failure(message: message) }
func requireUnlockedDesktop() throws {
    if NSWorkspace.shared.frontmostApplication?.bundleIdentifier == "com.apple.loginwindow" {
        try fail("Notes editor unavailable while the Mac is locked")
    }
}
func attr(_ e: AXUIElement, _ key: String) -> CFTypeRef? {
    var value: CFTypeRef?
    AXUIElementCopyAttributeValue(e, key as CFString, &value)
    return value
}
func elements(_ e: AXUIElement, _ depth: Int = 0) -> [AXUIElement] {
    if depth > 14 { return [] }
    return [e] + (attr(e, "AXChildren") as? [AXUIElement] ?? []).prefix(200).flatMap { elements($0, depth + 1) }
}
func set(_ e: AXUIElement, _ key: String, _ value: CFTypeRef) throws {
    try requireUnlockedDesktop()
    guard AXUIElementSetAttributeValue(e, key as CFString, value) == .success else { try fail("accessibility selection failed") }
}
func select(_ e: AXUIElement, _ location: Int, _ length: Int = 0) throws {
    var range = CFRange(location: location, length: length)
    try set(e, kAXFocusedAttribute, kCFBooleanTrue)
    try set(e, kAXSelectedTextRangeAttribute, AXValueCreate(.cfRange, &range)!)
}
func key(_ app: NSRunningApplication, _ code: CGKeyCode, _ flags: CGEventFlags) throws {
    try requireUnlockedDesktop()
    guard NSWorkspace.shared.frontmostApplication?.processIdentifier == app.processIdentifier else { try fail("Notes lost foreground; keyboard automation stopped") }
    for down in [true, false] {
        let event = CGEvent(keyboardEventSource: nil, virtualKey: code, keyDown: down)!
        event.flags = flags
        event.postToPid(app.processIdentifier)
    }
    Thread.sleep(forTimeInterval: 0.25)
}
func content(_ editor: AXUIElement) throws -> (String, [Item]) {
    guard let text = attr(editor, kAXValueAttribute) as? String else { try fail("missing editor text") }
    let ns = text as NSString
    if ns.length == 0 { return (text, []) }
    var range = CFRange(location: 0, length: ns.length)
    var value: CFTypeRef?
    guard AXUIElementCopyParameterizedAttributeValue(editor, "AXAttributedStringForRange" as CFString, AXValueCreate(.cfRange, &range)!, &value) == .success,
          let attributed = value as? NSAttributedString else { try fail("native checklist state unavailable") }
    var items: [Item] = []
    var offset = 0
    while offset < ns.length {
        let line = ns.lineRange(for: NSRange(location: offset, length: 0))
        let prefix = attributed.attribute(NSAttributedString.Key("AXListItemPrefix"), at: offset, effectiveRange: nil)
        let state = (prefix as? NSAttributedString)?.string ?? (prefix as? String ?? "")
        if state.hasPrefix("checklist item, ") {
            guard state == "checklist item, incomplete" || state == "checklist item, completed" else { try fail("unknown native checkbox state") }
            items.append(Item(text: ns.substring(with: line).trimmingCharacters(in: .newlines), checked: state == "checklist item, completed", location: line.location, length: line.length))
        }
        offset = NSMaxRange(line)
    }
    return (text, items)
}
func show(_ id: String) throws -> String {
    // The ID is data, never executable script source. No title lookup or personal-note enumeration.
    let source = "function run(a){const n=Application('Notes').notes.byId(a[0]);if(!n.exists()||n.id()!==a[0])throw Error('note not found');if(n.passwordProtected())throw Error('protected note');Application('Notes').show(n);Application('Notes').activate();return n.plaintext();}"
    let process = Process(); process.executableURL = URL(fileURLWithPath: "/usr/bin/osascript")
    process.arguments = ["-l", "JavaScript", "-e", source, id]
    let output = Pipe(); process.standardOutput = output
    try process.run()
    let data = output.fileHandleForReading.readDataToEndOfFile(); process.waitUntilExit()
    guard process.terminationStatus == 0, let text = String(data: data, encoding: .utf8) else { try fail("cannot show exact note") }
    return text.trimmingCharacters(in: .newlines)
}
func moveToRecentlyDeleted(_ id: String) throws -> [String: Any] {
    // Notes' delete command is recoverable: it moves an active note to Recently
    // Deleted. Validate the opaque ID in a read-only process first. The mutation
    // repeats those checks immediately before delete, then verifies the same ID
    // is in Recently Deleted. Notes may not expose that folder until the first
    // deletion, and its global notes collection still includes deleted notes.
    func execute(_ source: String) throws -> (Int32, Data) {
        let process = Process(); process.executableURL = URL(fileURLWithPath: "/usr/bin/osascript")
        process.arguments = ["-l", "JavaScript", "-e", source, id]
        let output = Pipe(); process.standardOutput = output
        try process.run()
        let data = output.fileHandleForReading.readDataToEndOfFile(); process.waitUntilExit()
        return (process.terminationStatus, data)
    }
    let preflight = """
    function run(a){
      const app=Application('Notes'), id=a[0], n=app.notes.byId(id);
      if(!n.exists()||n.id()!==id)throw Error('note not found');
      if(n.passwordProtected())throw Error('protected note');
      if(n.container().name()==='Recently Deleted')throw Error('note already in Recently Deleted');
      return id;
    }
    """
    let checked = try execute(preflight)
    guard checked.0 == 0,
          String(data: checked.1, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines) == id else {
        try fail("cannot validate exact active note")
    }
    let mutation = """
    function run(a){
      const app=Application('Notes'), id=a[0], n=app.notes.byId(id);
      if(!n.exists()||n.id()!==id)throw Error('note not found');
      if(n.passwordProtected())throw Error('protected note');
      if(n.container().name()==='Recently Deleted')throw Error('note already in Recently Deleted');
      app.delete(n);
      for(let attempt=0;attempt<20;attempt++){
        delay(0.25);
        const current=app.notes.byId(id);
        if(current.exists()&&current.id()===id&&current.container().name()==='Recently Deleted'){
          return JSON.stringify({id:id,deleted:true});
        }
        const folders=app.folders.whose({name:{_equals:'Recently Deleted'}})();
        const matches=[];
        for(const folder of folders){
          for(const candidate of folder.notes.whose({id:{_equals:id}})()){
            if(candidate.id()===id)matches.push(candidate);
          }
        }
        if(matches.length===1)return JSON.stringify({id:id,deleted:true});
        if(matches.length>1)throw Error('deleted note identity ambiguous');
      }
      throw Error('note move not verified');
    }
    """
    wrote = true
    let moved = try execute(mutation)
    guard moved.0 == 0,
          let object = try? JSONSerialization.jsonObject(with: moved.1) as? [String: Any],
          object["id"] as? String == id, object["deleted"] as? Bool == true else { try fail("cannot verify exact note in Recently Deleted") }
    return object
}
func label(_ e: AXUIElement) -> String {
    (attr(e, "AXAttributedDescription") as? NSAttributedString)?.string ?? (attr(e, "AXDescription") as? String ?? "")
}
func unique(_ root: AXUIElement, _ predicate: (AXUIElement) -> Bool) throws -> AXUIElement {
    let matches = elements(root).filter(predicate)
    guard matches.count == 1 else { try fail("missing or ambiguous native control") }
    return matches[0]
}
func click(_ e: AXUIElement, named operation: String = "control") throws {
    try requireUnlockedDesktop()
    guard let app = NSWorkspace.shared.frontmostApplication, app.bundleIdentifier == "com.apple.Notes" else { try fail("Notes is not foreground; native UI automation stopped") }
    // Never use global coordinate clicks: a share picker or focus change can
    // redirect them to an unrelated target. Labels in SwiftUI often resolve to
    // a static-text child, so walk only its Notes-owned ancestor chain.
    var current: AXUIElement? = e
    var sawEnabledAction = false
    for _ in 0..<5 {
        guard let candidate = current else { break }
        let role = attr(candidate, kAXRoleAttribute) as? String ?? ""
        // A toolbar's menu customizes the toolbar; it never activates its child.
        // Do not let a missing child action escape into a container operation.
        if ["AXToolbar", "AXWindow", "AXApplication"].contains(role) { break }
        var owner: pid_t = 0
        guard AXUIElementGetPid(candidate, &owner) == .success, owner == app.processIdentifier else { try fail("native control does not belong to foreground Notes") }
        var actionValues: CFArray?
        if AXUIElementCopyActionNames(candidate, &actionValues) == .success {
            let actions = (actionValues as? [String]) ?? []
            let action = actions.contains(kAXPressAction as String) ? kAXPressAction as String :
                (actions.contains(kAXShowMenuAction as String) ? kAXShowMenuAction as String : "")
            if !action.isEmpty && (attr(candidate, kAXEnabledAttribute) as? NSNumber)?.boolValue != false {
                sawEnabledAction = true
                if AXUIElementPerformAction(candidate, action as CFString) == .success {
                    Thread.sleep(forTimeInterval: 0.35)
                    return
                }
            }
        }
        guard let parent = attr(candidate, kAXParentAttribute) else { current = nil; continue }
        current = unsafeBitCast(parent, to: AXUIElement.self)
    }
    try fail(sawEnabledAction ? "\(operation) accessibility action failed" : "\(operation) and ancestors support neither safe press nor menu")
}
func clickVisible(_ e: AXUIElement, named operation: String) throws {
    try requireUnlockedDesktop()
    guard let app = NSWorkspace.shared.frontmostApplication, app.bundleIdentifier == "com.apple.Notes" else { try fail("Notes is not foreground; native UI automation stopped") }
    var owner: pid_t = 0
    guard AXUIElementGetPid(e, &owner) == .success, owner == app.processIdentifier else { try fail("native control does not belong to foreground Notes") }
    if let positionRef = attr(e, kAXPositionAttribute),
       let sizeRef = attr(e, kAXSizeAttribute) {
        let position = positionRef as! AXValue
        let size = sizeRef as! AXValue
        var point = CGPoint.zero
        var dimensions = CGSize.zero
        guard AXValueGetValue(position, .cgPoint, &point),
              AXValueGetValue(size, .cgSize, &dimensions),
              dimensions.width > 0, dimensions.height > 0 else { try fail("\(operation) has no visible bounds") }
        let target = CGPoint(x: point.x + dimensions.width / 2, y: point.y + dimensions.height / 2)
        var hit: AXUIElement?
        guard AXUIElementCopyElementAtPosition(AXUIElementCreateSystemWide(), Float(target.x), Float(target.y), &hit) == .success,
              let hit else { try fail("\(operation) hit target was not verified") }
        // SwiftUI can return an actionable child of the labelled button. Its
        // ancestry must reach that exact control, never merely the same window.
        var current: AXUIElement? = hit
        var verified = false
        for _ in 0..<5 {
            guard let candidate = current else { break }
            var pid: pid_t = 0
            guard AXUIElementGetPid(candidate, &pid) == .success, pid == owner else { break }
            if CFEqual(candidate, e) { verified = true; break }
            guard let parent = attr(candidate, kAXParentAttribute) else { break }
            current = unsafeBitCast(parent, to: AXUIElement.self)
        }
        guard verified else { try fail("\(operation) hit target was not verified") }
        var actions: CFArray?
        if AXUIElementCopyActionNames(hit, &actions) == .success,
           (actions as? [String] ?? []).contains(kAXPressAction as String) {
            guard (attr(hit, kAXEnabledAttribute) as? NSNumber)?.boolValue != false,
                  AXUIElementPerformAction(hit, kAXPressAction as CFString) == .success else { try fail("\(operation) accessibility action failed") }
            Thread.sleep(forTimeInterval: 0.35)
            return
        }
        guard let down = CGEvent(mouseEventSource: nil, mouseType: .leftMouseDown, mouseCursorPosition: target, mouseButton: .left),
              let up = CGEvent(mouseEventSource: nil, mouseType: .leftMouseUp, mouseCursorPosition: target, mouseButton: .left) else { try fail("\(operation) click could not be created") }
        down.post(tap: .cghidEventTap)
        up.post(tap: .cghidEventTap)
        return
    }
    try fail("\(operation) has no visible bounds")
}
func waitFor(_ root: AXUIElement, _ predicate: (AXUIElement) -> Bool) throws -> AXUIElement {
    for _ in 0..<20 {
        let matches = elements(root).filter(predicate)
        if matches.count == 1 { return matches[0] }
        if matches.count > 1 { try fail("ambiguous native control") }
        Thread.sleep(forTimeInterval: 0.2)
    }
    try fail("native dialog did not reach expected state")
}
func waitUntil(_ predicate: () -> Bool) throws {
    for _ in 0..<20 {
        if predicate() { return }
        Thread.sleep(forTimeInterval: 0.2)
    }
    try fail("native dialog did not reach expected state")
}
func waitUntilGone(_ root: AXUIElement, _ predicate: (AXUIElement) -> Bool) throws {
    for _ in 0..<20 {
        if !elements(root).contains(where: predicate) { return }
        Thread.sleep(forTimeInterval: 0.2)
    }
    try fail("native dialog did not close")
}
func sharing(_ request: Request, _ root: AXUIElement, _ app: NSRunningApplication) throws -> [String: Any] {
    guard let participants = request.participants, participants.count == 2,
          Set(participants).count == 2, participants.allSatisfy({ $0.first == "+" && $0.dropFirst().allSatisfy(\.isNumber) }) else { try fail("two exact authorized participants required") }
    guard !elements(root).contains(where: { ["AXSheet", "AXPopover"].contains(attr($0, kAXRoleAttribute) as? String ?? "") }) else { try fail("existing Notes modal; refusing to take ownership") }
    var collaborationVerified = false
    var result: [String: Any] = ["id": request.id, "participants": participants, "verified": true]
    defer {
        // Once recipient entry begins, dismissal can cancel an in-flight
        // collaboration. Only close UI after the collaboration is verified.
        if collaborationVerified {
            let sheets = elements(root).filter { attr($0, kAXRoleAttribute) as? String == "AXSheet" }
            if sheets.count == 1 {
                let cancel = elements(sheets[0]).filter { attr($0, kAXRoleAttribute) as? String == "AXButton" && label($0) == "Cancel" }
                if cancel.count == 1 { _ = AXUIElementPerformAction(cancel[0], kAXPressAction as CFString) }
            } else if NSWorkspace.shared.frontmostApplication?.processIdentifier == app.processIdentifier,
                      elements(root).contains(where: { attr($0, kAXRoleAttribute) as? String == "AXPopover" }) {
                try? key(app, 53, [])
            }
        }
    }
    if request.operation == "share" {
        // This creates invitations only. Never re-invite or modify existing collaborations.
        if elements(root).contains(where: { label($0) == "Collaborate" }) {
            try fail("note already shared; use shared_link")
        }
        try click(unique(root) {
            (attr($0, kAXRoleAttribute) as? String) == (kAXButtonRole as String) &&
            label($0) == "Share" &&
            (attr($0, kAXEnabledAttribute) as? NSNumber)?.boolValue != false
        }, named: "Share")
        let popup = try waitFor(root) { attr($0, kAXRoleAttribute) as? String == "AXPopUpButton" && attr($0, kAXValueAttribute) as? String == "Collaborate" }
        _ = popup
        try click(unique(root) {
            label($0) == "Invite with Link" &&
            ["AXButton", "AXMenuItem"].contains(attr($0, kAXRoleAttribute) as? String ?? "")
        }, named: "Invite with Link")
        let sheet = try waitFor(root) { attr($0, kAXRoleAttribute) as? String == "AXSheet" }
        let field = try unique(sheet) { attr($0, kAXRoleAttribute) as? String == "AXTextField" }
        var tokenNames: [String] = []
        for (index, phone) in participants.enumerated() {
            try select(field, index)
            try set(field, kAXSelectedTextAttribute, phone as CFString)
            try key(app, 36, [])
            let suggestion = try waitFor(root) {
                attr($0, kAXRoleAttribute) as? String == "AXStaticText" && (attr($0, kAXValueAttribute) as? String ?? "").hasPrefix(phone + " (")
            }
            let value = attr(suggestion, kAXValueAttribute) as! String
            tokenNames.append(String(value.dropFirst(phone.count + 2).dropLast()))
            try clickVisible(suggestion, named: "recipient suggestion")
            try waitUntil {
                let current = attr(field, kAXValueAttribute) as? String ?? ""
                return current == String(repeating: "\u{fffc}", count: index + 1) ||
                    elements(sheet).contains {
                        (attr($0, kAXValueAttribute) as? String) == tokenNames.last || label($0) == tokenNames.last
                    }
            }
        }
        let sheetElements = elements(sheet)
        for name in tokenNames {
            let matches = sheetElements.filter {
                (attr($0, kAXValueAttribute) as? String) == name || label($0) == name
            }
            guard matches.count >= 1 else { try fail("recipient identity ambiguous") }
        }
        let clipboardBaseline = NSPasteboard.general.changeCount
        wrote = true
        let copyControls = elements(sheet).filter { label($0) == "Copy Link" }
        guard let copyControl = copyControls.first(where: {
            (attr($0, kAXEnabledAttribute) as? NSNumber)?.boolValue == true &&
            ["AXButton", "AXMenuItem"].contains(attr($0, kAXRoleAttribute) as? String ?? "")
        }) else { try fail("enabled Create Link control not found") }
        try click(copyControl, named: "Create Link")
        var collaborationLink: String?
        for _ in 0..<100 {
            if NSPasteboard.general.changeCount != clipboardBaseline,
               let candidate = NSPasteboard.general.string(forType: .string),
               let url = URL(string: candidate), url.scheme == "https",
               url.host == "www.icloud.com", url.path.hasPrefix("/notes/") {
                collaborationLink = candidate
                break
            }
            Thread.sleep(forTimeInterval: 0.1)
        }
        guard let collaborationLink else { try fail("fresh collaboration link was not captured while sharing remained open") }
        // Copy Link leaves the creation sheet open on macOS 26. Close it only
        // after validating the fresh URL, then verify persisted collaboration.
        try key(app, 53, [])
        try waitUntilGone(root) { attr($0, kAXRoleAttribute) as? String == "AXSheet" }
        result["link"] = collaborationLink
    }
    // macOS 26 exposes this button without AXPress. Click only its verified
    // visible bounds, after exact-note selection, with an exact hit-test.
    try clickVisible(unique(root) { attr($0, kAXRoleAttribute) as? String == "AXButton" && label($0) == "Collaborate" }, named: "Collaborate")
    try click(waitFor(root) { label($0) == "Manage Shared Note" }, named: "Manage Shared Note")
    let panel = try waitFor(root) { attr($0, "AXIdentifier") as? String == "share settings" }
    let names = elements(panel).filter { attr($0, "AXIdentifier") as? String == "participantName" }.compactMap { attr($0, kAXValueAttribute) as? String }
    for phone in participants { guard names.contains(String(phone.dropFirst())) else { try fail("persisted participant missing; reconcile sharing manually") } }
    guard names.count == 3, elements(panel).contains(where: { attr($0, kAXValueAttribute) as? String == "Only people you invite" }) else { try fail("unexpected collaboration membership or access") }
    if request.operation == "share", let expand = elements(panel).first(where: { label($0) == "Anyone can add more people" }), (attr(expand, kAXValueAttribute) as? NSNumber)?.boolValue == true {
        try click(expand, named: "participant expansion")
        guard (attr(expand, kAXValueAttribute) as? NSNumber)?.boolValue == false else { try fail("participant expansion setting not verified") }
    }
    try click(unique(panel) { label($0) == "Done" }, named: "Done")
    collaborationVerified = true
    if request.operation == "shared_link" {
        try clickVisible(unique(root) { attr($0, kAXRoleAttribute) as? String == "AXButton" && label($0) == "Collaborate" }, named: "Collaborate")
        let copy = try waitFor(root) { label($0) == "Copy Link" }
        let previous = NSPasteboard.general.changeCount
        try click(copy, named: "Copy Link")
        for _ in 0..<40 {
            if NSPasteboard.general.changeCount != previous { break }
            Thread.sleep(forTimeInterval: 0.1)
        }
        guard NSPasteboard.general.changeCount != previous,
              let link = NSPasteboard.general.string(forType: .string),
              let url = URL(string: link), url.scheme == "https", url.host == "www.icloud.com", url.path.hasPrefix("/notes/") else { try fail("fresh iCloud Notes link not available") }
        result["link"] = link
    }
    return result
}
func run(_ request: Request) throws -> [String: Any] {
    guard ["checklist", "edit_text", "edit_checklist_item", "add_checklist_item", "set_checked", "share", "participants", "shared_link", "move_to_recently_deleted"].contains(request.operation) else { try fail("unsupported operation") }
    let lock = open((NSHomeDirectory() + "/Library/Caches/teslashibe-notes.lock"), O_CREAT | O_RDWR | O_NOFOLLOW, S_IRUSR | S_IWUSR)
    guard lock >= 0, flock(lock, LOCK_EX | LOCK_NB) == 0 else { try fail("Notes automation busy") }
    defer { close(lock) }
    guard AXIsProcessTrusted() else { try fail("Accessibility permission required") }
    if request.operation == "move_to_recently_deleted" { return try moveToRecentlyDeleted(request.id) }
    try requireUnlockedDesktop()
    let expected = try show(request.id)
    guard let app = NSRunningApplication.runningApplications(withBundleIdentifier: "com.apple.Notes").first else { try fail("Notes not running") }
    let root = AXUIElementCreateApplication(app.processIdentifier)
    guard AXUIElementSetMessagingTimeout(root, 2) == .success else { try fail("cannot bound Notes accessibility calls") }
    var matched: [AXUIElement] = []
    for attempt in 0..<15 {
        try requireUnlockedDesktop()
        let all = elements(root)
        guard !all.contains(where: { attr($0, kAXRoleAttribute) as? String == "AXSheet" }) else { try fail("Notes has a modal dialog; finish it manually") }
        let editors = all.filter { attr($0, "AXIdentifier") as? String == "Note Body Text View" }
        var unreadable = false
        matched = editors.filter {
            guard let current = try? content($0).0 else {
                unreadable = true
                return false
            }
            return current.trimmingCharacters(in: .newlines) == expected
        }
        if matched.count == 1 && !unreadable { break }
        if matched.count > 1 || attempt == 14 { try fail("ambiguous note editor") }
        Thread.sleep(forTimeInterval: 0.2)
    }
    let editor = matched[0]
    let before = try content(editor)
    guard before.0.trimmingCharacters(in: .newlines) == expected else { try fail("note editor identity or content changed") }
    if request.operation == "share" || request.operation == "participants" || request.operation == "shared_link" {
        try requireUnlockedDesktop()
        return try sharing(request, root, app)
    }
    if request.operation == "edit_text" {
        guard let text = request.text, !text.isEmpty,
              !text.contains("\u{fffc}"), !(request.replacement ?? "").contains("\u{fffc}") else { try fail("invalid text range") }
        let replacement = request.replacement ?? ""
        let original = before.0 as NSString
        let target = original.range(of: text, options: .literal)
        guard target.location != NSNotFound else { try fail("text missing or ambiguous") }
        // Search from the next UTF-16 position to reject overlapping duplicates too.
        let remainder = NSRange(location: target.location + 1, length: original.length - target.location - 1)
        guard original.range(of: text, options: .literal, range: remainder).location == NSNotFound else { try fail("text missing or ambiguous") }
        guard !before.1.contains(where: { NSIntersectionRange(target, NSRange(location: $0.location, length: $0.length)).length > 0 }) else { try fail("use checklist editing for checklist text") }
        if text != replacement {
            try requireUnlockedDesktop()
            try select(editor, target.location, target.length)
            let current = try content(editor)
            guard current.0 == before.0 && current.1 == before.1 else { try fail("note changed before text edit") }
            wrote = true
            try set(editor, kAXSelectedTextAttribute, replacement as CFString)
            let after = try content(editor)
            guard after.0 == original.replacingCharacters(in: target, with: replacement),
                  after.1.count == before.1.count,
                  zip(after.1, before.1).allSatisfy({ current, previous in current.text == previous.text && current.checked == previous.checked }) else { try fail("text edit or preserved checklist not verified") }
        }
    } else if request.operation == "edit_checklist_item" {
        guard let old = request.text, !old.isEmpty, let replacement = request.replacement, !replacement.isEmpty else { try fail("exact checklist replacement required") }
        let matches = before.1.filter { $0.text == old }
        guard matches.count == 1 else { try fail("checklist item missing or ambiguous") }
        guard !before.1.contains(where: { $0.text == replacement && $0.location != matches[0].location }) else { try fail("replacement would duplicate checklist item") }
        let target = matches[0]
        let source = before.0 as NSString
        let newlineLength = target.length - (target.text as NSString).length
        guard newlineLength >= 0 else { try fail("invalid checklist item range") }
        try requireUnlockedDesktop()
        try select(editor, target.location, target.length - newlineLength)
        guard try content(editor).0 == before.0 else { try fail("note changed before edit") }
        try requireUnlockedDesktop()
        wrote = true
        try set(editor, kAXSelectedTextAttribute, replacement as CFString)
        let after = try content(editor)
        let expectedText = source.replacingCharacters(in: NSRange(location: target.location, length: target.length - newlineLength), with: replacement)
        guard after.0 == expectedText, after.1.count == before.1.count,
              after.1.filter({ $0.text == replacement && $0.checked == target.checked }).count == 1,
              zip(after.1, before.1).allSatisfy({ current, previous in
                  previous.location == target.location || (current.text == previous.text && current.checked == previous.checked)
              }) else { try fail("replacement or native checklist state not verified") }
    } else if request.operation == "set_checked" {
        let matches = before.1.filter { $0.text == request.text }
        guard matches.count == 1, let checked = request.checked else { try fail("item missing or ambiguous") }
        if matches[0].checked != checked {
            try requireUnlockedDesktop()
            try select(editor, matches[0].location)
            let current = try content(editor)
            guard current.0 == before.0 && current.1 == before.1 else { try fail("note changed before mutation") }
            try requireUnlockedDesktop()
            wrote = true
            try key(app, 32, [.maskCommand, .maskShift])
            let after = try content(editor)
            let updated = after.1.filter { $0.text == request.text }
            guard updated.count == 1, updated[0].checked == checked else { try fail("checkbox mutation not verified") }
            let expectedItems = before.1.map { Item(text: $0.text, checked: $0.text == request.text ? checked : $0.checked, location: 0, length: 0) }.sorted { $0.text < $1.text }
            let actualItems = after.1.map { Item(text: $0.text, checked: $0.checked, location: 0, length: 0) }.sorted { $0.text < $1.text }
            guard actualItems == expectedItems else { try fail("concurrent checklist change; reconcile") }
        }
    } else if request.operation == "add_checklist_item" {
        guard let text = request.text, !text.isEmpty, !text.contains(where: { $0.isNewline }), !before.1.contains(where: { $0.text == text }) else { try fail("empty, multiline, or duplicate item") }
        let prefix = before.0.hasSuffix("\n") ? "" : "\n"
        let location = (before.0 as NSString).length + (prefix as NSString).length
        try requireUnlockedDesktop()
        try select(editor, (before.0 as NSString).length)
        guard try content(editor).0 == before.0 else { try fail("note changed before append") }
        try requireUnlockedDesktop()
        wrote = true
        try set(editor, kAXSelectedTextAttribute, (prefix + text + "\n") as CFString)
        try requireUnlockedDesktop()
        try select(editor, location)
        if !(try content(editor).1.contains(where: { $0.location == location })) {
            try requireUnlockedDesktop()
            try key(app, 37, [.maskCommand, .maskShift])
        }
        let after = try content(editor)
        guard after.0 == before.0 + prefix + text + "\n", after.1.filter({ $0.text == text && !$0.checked }).count == 1 else { try fail("native checklist append not verified") }
    }
    let result = try content(editor)
    let data = try JSONEncoder().encode(result.1)
    return ["id": request.id, "items": try JSONSerialization.jsonObject(with: data), "verified": true]
}
do {
    let request = try JSONDecoder().decode(Request.self, from: FileHandle.standardInput.readDataToEndOfFile())
    let output = try run(request)
    FileHandle.standardOutput.write(try JSONSerialization.data(withJSONObject: output))
} catch {
    let message = (error as? Failure)?.message ?? "invalid request or native automation failure"
    let output: [String: Any] = ["error": message, "uncertain": wrote]
    FileHandle.standardOutput.write(try! JSONSerialization.data(withJSONObject: output))
    exit(1)
}

import Foundation

public struct AppPaths: @unchecked Sendable {
  /// On-disk identity predates the "Doublangu worker" product rename; changing
  /// it would strand the config, models, and logs of existing installs.
  public static let productDirectory = "Doublangu Speech Worker"

  private let fileManager: FileManager
  private let rootOverride: URL?

  public init(fileManager: FileManager = .default, rootOverride: URL? = nil) {
    self.fileManager = fileManager
    self.rootOverride = rootOverride
  }

  public var applicationSupportRoot: URL {
    rootOverride
      ?? directory(.applicationSupportDirectory).appendingPathComponent(
        Self.productDirectory, isDirectory: true)
  }

  public var cachesRoot: URL {
    rootOverride.map { $0.appendingPathComponent("Caches", isDirectory: true) }
      ?? directory(.cachesDirectory).appendingPathComponent(
        Self.productDirectory, isDirectory: true)
  }

  public var logsRoot: URL {
    rootOverride.map { $0.appendingPathComponent("Logs", isDirectory: true) }
      ?? directory(.libraryDirectory).appendingPathComponent(
        "Logs/Doublangu Speech Worker", isDirectory: true)
  }

  public var configURL: URL { applicationSupportRoot.appendingPathComponent("config.json") }
  public var referenceURL: URL {
    applicationSupportRoot.appendingPathComponent("Reference/dutch-reference-v1.wav")
  }
  public var modelsRoot: URL {
    applicationSupportRoot.appendingPathComponent("Models", isDirectory: true)
  }
  public var spoolRoot: URL {
    applicationSupportRoot.appendingPathComponent("Spool", isDirectory: true)
  }
  public var stateRoot: URL {
    applicationSupportRoot.appendingPathComponent("State", isDirectory: true)
  }
  public var setupReceiptURL: URL { stateRoot.appendingPathComponent("setup.json") }
  public var modelReceiptURL: URL { stateRoot.appendingPathComponent("model-receipt.json") }
  public var workerReceiptURL: URL { stateRoot.appendingPathComponent("worker.json") }
  public var logURL: URL { logsRoot.appendingPathComponent("worker.log") }
  public var downloadCacheRoot: URL {
    cachesRoot.appendingPathComponent("model-download", isDirectory: true)
  }

  public var bundledRuntimeRoot: URL {
    (Bundle.main.resourceURL ?? URL(fileURLWithPath: ".")).appendingPathComponent(
      "runtime", isDirectory: true)
  }

  public var bundledPythonURL: URL { bundledRuntimeRoot.appendingPathComponent("venv/bin/python") }
  public var bundledLockURL: URL { bundledRuntimeRoot.appendingPathComponent("receipt/uv.lock") }

  public func modelURL(revision: String) -> URL {
    modelsRoot.appendingPathComponent(revision, isDirectory: true)
  }

  public func journalURL(jobID: String) -> URL { spoolRoot.appendingPathComponent("\(jobID).json") }
  public func partialURL(jobID: String) -> URL {
    spoolRoot.appendingPathComponent("\(jobID).partial")
  }
  public func readyURL(jobID: String) -> URL { spoolRoot.appendingPathComponent("\(jobID).ready") }

  public func ensureDirectories() throws {
    for url in [
      applicationSupportRoot, cachesRoot, logsRoot, modelsRoot, spoolRoot, stateRoot,
      downloadCacheRoot, referenceURL.deletingLastPathComponent(),
    ] {
      try fileManager.createDirectory(
        at: url, withIntermediateDirectories: true,
        attributes: [.posixPermissions: NSNumber(value: Int16(0o700))])
      try fileManager.setAttributes(
        [.posixPermissions: NSNumber(value: Int16(0o700))], ofItemAtPath: url.path)
    }
    var values = URLResourceValues()
    values.isExcludedFromBackup = true
    var models = modelsRoot
    try models.setResourceValues(values)
    var downloads = downloadCacheRoot
    try downloads.setResourceValues(values)
  }

  public func writePrivate(_ data: Data, to url: URL) throws {
    try ensureParent(url)
    let temporary = url.deletingLastPathComponent().appendingPathComponent(
      ".\(url.lastPathComponent).\(UUID().uuidString).partial")
    try data.write(to: temporary, options: .atomic)
    try fileManager.setAttributes(
      [.posixPermissions: NSNumber(value: Int16(0o600))], ofItemAtPath: temporary.path)
    if fileManager.fileExists(atPath: url.path) {
      _ = try fileManager.replaceItemAt(url, withItemAt: temporary)
    } else {
      try fileManager.moveItem(at: temporary, to: url)
    }
    try fileManager.setAttributes(
      [.posixPermissions: NSNumber(value: Int16(0o600))], ofItemAtPath: url.path)
  }

  public func ensureParent(_ url: URL) throws {
    let parent = url.deletingLastPathComponent()
    try fileManager.createDirectory(
      at: parent, withIntermediateDirectories: true,
      attributes: [.posixPermissions: NSNumber(value: Int16(0o700))])
    try fileManager.setAttributes(
      [.posixPermissions: NSNumber(value: Int16(0o700))], ofItemAtPath: parent.path)
  }

  public func freeBytes() -> Int64? {
    var url = applicationSupportRoot
    while !fileManager.fileExists(atPath: url.path) {
      let parent = url.deletingLastPathComponent()
      guard parent != url else { return nil }
      url = parent
    }
    return (try? url.resourceValues(forKeys: [.volumeAvailableCapacityForImportantUsageKey]))?
      .volumeAvailableCapacityForImportantUsage
  }

  public func modelRevisionDirectories() -> [URL] {
    guard
      let urls = try? fileManager.contentsOfDirectory(
        at: modelsRoot, includingPropertiesForKeys: [.isDirectoryKey, .isSymbolicLinkKey],
        options: [.skipsHiddenFiles])
    else { return [] }
    return urls.filter {
      guard let values = try? $0.resourceValues(forKeys: [.isDirectoryKey, .isSymbolicLinkKey])
      else { return false }
      return values.isDirectory == true && values.isSymbolicLink != true
    }
  }

  private func directory(_ kind: FileManager.SearchPathDirectory) -> URL {
    fileManager.urls(for: kind, in: .userDomainMask).first ?? URL(fileURLWithPath: ".")
  }
}

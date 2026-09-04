import Foundation

/// Timestamped, single-line app log on top of `LogRotation`. Writes are best
/// effort: a failed log write must never take the worker down.
public final class WorkerLogger: @unchecked Sendable {
  public static let maximumLineUTF8Bytes = 2_000
  private let rotation: LogRotation
  private let clock: () -> Date
  private let formatter: ISO8601DateFormatter

  public init(logURL: URL, clock: @escaping () -> Date = Date.init) {
    rotation = LogRotation(logURL: logURL)
    self.clock = clock
    formatter = ISO8601DateFormatter()
    formatter.formatOptions = [.withInternetDateTime]
  }

  public func write(_ message: String) {
    var compact = message.replacingOccurrences(of: "\n", with: " ").trimmingCharacters(
      in: .whitespaces)
    if compact.isEmpty { return }
    if compact.utf8.count > Self.maximumLineUTF8Bytes {
      var truncated = ""
      var bytes = 0
      for character in compact {
        let width = String(character).utf8.count
        if bytes + width > Self.maximumLineUTF8Bytes { break }
        truncated.append(character)
        bytes += width
      }
      compact = truncated
    }
    let line = "\(formatter.string(from: clock())) \(compact)\n"
    try? rotation.append(Data(line.utf8))
  }
}

public final class LogRotation: @unchecked Sendable {
  public let logURL: URL
  public let maximumBytes: Int64
  public let retainedFiles: Int
  private let fileManager: FileManager
  private let lock = NSLock()

  public init(
    logURL: URL, maximumBytes: Int64 = 10 * 1024 * 1024, retainedFiles: Int = 5,
    fileManager: FileManager = .default
  ) {
    self.logURL = logURL
    self.maximumBytes = maximumBytes
    self.retainedFiles = max(1, retainedFiles)
    self.fileManager = fileManager
  }

  public func rotateIfNeeded() throws {
    lock.lock()
    defer { lock.unlock() }
    try rotateIfNeededLocked()
  }

  public func append(_ data: Data) throws {
    lock.lock()
    defer { lock.unlock() }
    try fileManager.createDirectory(
      at: logURL.deletingLastPathComponent(), withIntermediateDirectories: true,
      attributes: [.posixPermissions: NSNumber(value: Int16(0o700))])
    try rotateIfNeededLocked()
    if !fileManager.fileExists(atPath: logURL.path) {
      _ = fileManager.createFile(
        atPath: logURL.path, contents: Data(),
        attributes: [.posixPermissions: NSNumber(value: Int16(0o600))])
    }
    let handle = try FileHandle(forWritingTo: logURL)
    defer { try? handle.close() }
    try handle.seekToEnd()
    try handle.write(contentsOf: data)
    try handle.synchronize()
    try fileManager.setAttributes(
      [.posixPermissions: NSNumber(value: Int16(0o600))], ofItemAtPath: logURL.path)
    try rotateIfNeededLocked()
  }

  public func rotatedURL(_ index: Int) -> URL {
    logURL.deletingPathExtension().appendingPathExtension("log.\(index)")
  }

  private func rotateIfNeededLocked() throws {
    guard fileManager.fileExists(atPath: logURL.path),
      let size = try? fileManager.attributesOfItem(atPath: logURL.path)[.size] as? NSNumber,
      size.int64Value >= maximumBytes
    else { return }
    for index in stride(from: retainedFiles - 1, through: 1, by: -1) {
      let source = rotatedURL(index)
      let destination = rotatedURL(index + 1)
      if fileManager.fileExists(atPath: destination.path) {
        try fileManager.removeItem(at: destination)
      }
      if fileManager.fileExists(atPath: source.path) {
        try fileManager.moveItem(at: source, to: destination)
      }
    }
    let first = rotatedURL(1)
    if fileManager.fileExists(atPath: first.path) { try fileManager.removeItem(at: first) }
    try fileManager.moveItem(at: logURL, to: first)
    _ = fileManager.createFile(
      atPath: logURL.path, contents: Data(),
      attributes: [.posixPermissions: NSNumber(value: Int16(0o600))])
  }
}

import Foundation
import Security

public enum KeychainError: Error, Equatable, LocalizedError, Sendable {
  case notFound
  case unexpectedStatus(OSStatus)
  case invalidData

  public var errorDescription: String? {
    switch self {
    case .notFound: "keychain_item_not_found"
    case .unexpectedStatus: "keychain_unexpected_status"
    case .invalidData: "keychain_invalid_data"
    }
  }
}

public protocol SecretStore: AnyObject, Sendable {
  func read(account: String) throws -> String?
  func write(_ value: String, account: String) throws
  func delete(account: String) throws
}

public final class KeychainStore: SecretStore, @unchecked Sendable {
  public let service: String
  public init(service: String = WorkerConstants.bundleIdentifier) { self.service = service }

  public func read(account: String) throws -> String? {
    let query: [String: Any] = [
      kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service,
      kSecAttrAccount as String: account, kSecReturnData as String: true,
      kSecMatchLimit as String: kSecMatchLimitOne,
    ]
    var result: CFTypeRef?
    let status = SecItemCopyMatching(query as CFDictionary, &result)
    if status == errSecItemNotFound { return nil }
    guard status == errSecSuccess else { throw KeychainError.unexpectedStatus(status) }
    guard let data = result as? Data, let value = String(data: data, encoding: .utf8) else {
      throw KeychainError.invalidData
    }
    return value
  }

  public func write(_ value: String, account: String) throws {
    let data = Data(value.utf8)
    let query: [String: Any] = [
      kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service,
      kSecAttrAccount as String: account,
    ]
    let status = SecItemUpdate(
      query as CFDictionary, [kSecValueData as String: data] as CFDictionary)
    if status == errSecSuccess { return }
    guard status == errSecItemNotFound else { throw KeychainError.unexpectedStatus(status) }
    var add = query
    add[kSecValueData as String] = data
    let addStatus = SecItemAdd(add as CFDictionary, nil)
    guard addStatus == errSecSuccess else { throw KeychainError.unexpectedStatus(addStatus) }
  }

  public func delete(account: String) throws {
    let query: [String: Any] = [
      kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service,
      kSecAttrAccount as String: account,
    ]
    let status = SecItemDelete(query as CFDictionary)
    guard status == errSecSuccess || status == errSecItemNotFound else {
      throw KeychainError.unexpectedStatus(status)
    }
  }
}

public enum KeychainAccount {
  public static let perimeterUsername = "perimeter-username"
  public static let perimeterPassword = "perimeter-password"
  public static let workerToken = "worker-token"
}

public final class MemorySecretStore: SecretStore, @unchecked Sendable {
  private var values: [String: String] = [:]
  private let lock = NSLock()

  public init() {}

  public func read(account: String) throws -> String? {
    lock.lock()
    defer { lock.unlock() }
    return values[account]
  }
  public func write(_ value: String, account: String) throws {
    lock.lock()
    values[account] = value
    lock.unlock()
  }
  public func delete(account: String) throws {
    lock.lock()
    values.removeValue(forKey: account)
    lock.unlock()
  }
}

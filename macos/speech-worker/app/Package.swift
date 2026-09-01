// swift-tools-version: 6.0

import PackageDescription

let package = Package(
  name: "DoublanguSpeechWorker",
  platforms: [.macOS(.v14)],
  products: [
    .executable(name: "DoublanguSpeechWorker", targets: ["DoublanguSpeechWorker"])
  ],
  targets: [
    .executableTarget(
      name: "DoublanguSpeechWorker",
      path: "Sources/DoublanguSpeechWorker"
    ),
    .testTarget(
      name: "DoublanguSpeechWorkerTests",
      dependencies: ["DoublanguSpeechWorker"],
      path: "Tests/DoublanguSpeechWorkerTests"
    ),
  ],
  swiftLanguageModes: [.v6]
)

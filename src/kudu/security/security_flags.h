// Licensed to the Apache Software Foundation (ASF) under one
// or more contributor license agreements.  See the NOTICE file
// distributed with this work for additional information
// regarding copyright ownership.  The ASF licenses this file
// to you under the Apache License, Version 2.0 (the
// "License"); you may not use this file except in compliance
// with the License.  You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.
#pragma once

#include "kudu/util/flags.h"

namespace kudu {
namespace security {

// Authentication configuration for RPC connections.
typedef TriStateFlag RpcAuthentication;

// Encryption configuration for RPC connections.
typedef TriStateFlag RpcEncryption;

struct SecurityDefaults {
  // The names for the 'kDefaultTlsCiphers' and 'kDefaultTlsCipherSuites'
  // constants are confusingly close, but likely 'kDefaultTlsCiphers' is likely
  // to be removed when obsoleting TLSv1.2 at some point in the future.
  static const char* const kDefaultTlsCiphers;      // pre-TLSv1.3 ciphers
  static const char* const kDefaultTlsCipherSuites; // TLSv1.3 and later ciphers
  static const char* const kDefaultTlsMinVersion;
};

} // namespace security
} // namespace kudu

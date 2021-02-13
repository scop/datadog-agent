#
# Copyright 2012-2014 Chef Software, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#

name "rpm"
default_version "4.16.0"

license "LGPLv2"
license_file "COPYING"
skip_transitive_dependency_licensing true

dependency "config_guess"
dependency "elfutils"
dependency "file"
dependency "libgpg-error"
dependency "libgcrypt"
dependency "popt"
dependency "zstd"

version "4.16.0" do
  source url: "http://ftp.rpm.org/releases/rpm-4.16.x/rpm-4.16.0.tar.bz2",
         sha256: "ca5974e9da2939afb422598818ef187385061889ba766166c4a3829c5ef8d411"
end

relative_path "rpm-#{version}"

build do
  env = with_standard_compiler_flags(with_embedded_path)

  update_config_guess

  configure_options = [
    "--enable-bdb=no",
    "--enable-sqlite=no",
    "--disable-nls",
    "--disable-openmp",
    "--disable-plugins",
    "--without-archive",
    "--without-selinux",
    "--without-imaevm",
    "--without-cap",
    "--without-acl",
    "--without-lua",
    "--without-audit",
    ]
  configure(*configure_options, env: env)

  make "-j #{workers}", env: env
  make "install", env: env
end

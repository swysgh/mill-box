pluginManagement {
    repositories {
        maven("https://maven.aliyun.com/repository/google")
        maven("https://maven.aliyun.com/repository/gradle-plugin")
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}
dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        maven {
            url = uri("https://jitpack.io")
            content { includeGroupByRegex("com\\.github\\..*") }
        }
        maven("https://maven.aliyun.com/repository/google")
        mavenCentral()
        maven("https://maven.aliyun.com/repository/central")
        maven {
            url = uri("https://central.sonatype.com/repository/maven-snapshots/")
            mavenContent { snapshotsOnly() }
            content { includeGroup("io.github.sagernet") }
        }
        google()
        maven { url = uri("https://api.xposed.info/") }
    }
}
rootProject.name = "sing-box"
include(":app")
include(":libxposed-api")
project(":libxposed-api").projectDir = file("third_party/libxposed-api")

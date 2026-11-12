plugins { id("com.android.application") }

android {
    namespace = "dev.chatagent.ai"
    compileSdk = 36
    defaultConfig {
        applicationId = "dev.chatagent.ai"
        minSdk = 26
        targetSdk = 36
        versionCode = providers.gradleProperty("chatVersionCode").orElse("28").get().toInt()
        versionName = providers.gradleProperty("chatVersionName").orElse("0.6.1").get()
    }
    buildFeatures { buildConfig = true }
    buildTypes {
        debug {
            applicationIdSuffix = ".debug"
            versionNameSuffix = "-debug"
        }
        release { isMinifyEnabled = false }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}
kotlin {
    compilerOptions { jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17) }
}
dependencies { testImplementation("junit:junit:4.13.2") }

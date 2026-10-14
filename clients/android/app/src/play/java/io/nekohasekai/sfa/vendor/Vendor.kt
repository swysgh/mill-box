package io.nekohasekai.sfa.vendor

import android.util.Log
import androidx.camera.core.ImageAnalysis
import com.google.mlkit.common.MlKitException
import io.nekohasekai.sfa.compose.screen.qrscan.QRCodeCropArea

object Vendor : VendorInterface {
    private const val TAG = "Vendor"

    override fun createQRCodeAnalyzer(
        onSuccess: (String) -> Unit,
        onFailure: (Exception) -> Unit,
        onCropArea: ((QRCodeCropArea?) -> Unit)?,
    ): ImageAnalysis.Analyzer? {
        try {
            return MLKitQRCodeAnalyzer(onSuccess, onFailure, onCropArea)
        } catch (exception: Exception) {
            if (exception !is MlKitException || exception.errorCode != MlKitException.UNAVAILABLE) {
                Log.e(TAG, "failed to create MLKitQRCodeAnalyzer", exception)
            }
            return null
        }
    }
}
